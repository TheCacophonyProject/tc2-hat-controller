package main

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/godbus/dbus"
	"github.com/godbus/dbus/introspect"
	"github.com/sirupsen/logrus"
	"periph.io/x/conn/v3/gpio"
	"periph.io/x/conn/v3/gpio/gpioreg"
	"periph.io/x/conn/v3/i2c"
	"periph.io/x/conn/v3/i2c/i2creg"
	"periph.io/x/host/v3"
)

const (
	dbusName = "org.cacophony.i2c"
	dbusPath = "/org/cacophony/i2c"
)

type service struct {
	requests     chan Request // Channel to queue requests
	busyPin      gpio.PinIO
	bus          i2c.Bus
	mutex        sync.Mutex
	log          *logrus.Logger
	requestCount int
}

func startService(log *logrus.Logger) error {
	log.Info("Starting I2C service")
	conn, err := dbus.SystemBus()
	if err != nil {
		return err
	}
	reply, err := conn.RequestName(dbusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return errors.New("name already taken")
	}

	log.Debug("Initializing host")
	if _, err := host.Init(); err != nil {
		return err
	}
	bus, err := i2creg.Open("")
	if err != nil {
		return err
	}

	pinName := "GPIO13"
	log.Debugf("Initializing pin '%s'", pinName)
	pin := gpioreg.ByName(pinName)
	if pin == nil {
		return fmt.Errorf("GPIO pin %s not found", pinName)
	}
	if err := pin.In(gpio.Float, gpio.NoEdge); err != nil {
		return err
	}

	s := &service{
		busyPin:  pin,
		bus:      bus,
		mutex:    sync.Mutex{},
		log:      log,
		requests: make(chan Request, 20),
	}

	// Start a goroutine to process requests sequentially
	go func() {
		for req := range s.requests {
			res := s.processTransaction(req)
			req.Response <- res
		}
	}()

	conn.Export(s, dbusPath, dbusName)
	conn.Export(genIntrospectable(s), dbusPath, "org.freedesktop.DBus.Introspectable")
	return nil
}

func genIntrospectable(v interface{}) introspect.Introspectable {
	node := &introspect.Node{
		Interfaces: []introspect.Interface{{
			Name:    dbusName,
			Methods: introspect.Methods(v),
		}},
	}
	return introspect.NewIntrospectable(node)
}

func (s *service) Tx(address byte, write []byte, readLen int, timeout int) ([]byte, *dbus.Error) {
	s.mutex.Lock()
	requestID := s.requestCount
	s.requestCount++
	s.mutex.Unlock()

	responseChan := make(chan Response, 1)
	request := Request{
		RequestTime: time.Now(),
		RequestID:   requestID,
		Address:     address,
		Write:       write,
		ReadLen:     readLen,
		Timeout:     timeout,
		Response:    responseChan,
	}
	s.log.Debugf("Adding request '%d' to the queue", requestID)
	s.requests <- request // Enqueue the request

	// Wait for the response
	response := <-responseChan
	return response.Data, response.Err
}

type Request struct {
	RequestTime time.Time
	RequestID   int
	Address     byte
	Write       []byte
	ReadLen     int
	Timeout     int
	Response    chan Response // Channel for sending back the response
}

type Response struct {
	Data []byte
	Err  *dbus.Error
}

func (s *service) processTransaction(req Request) Response {
	startTime := time.Now()
	s.log.Debugf("Waited %s for request to be processed.", startTime.Sub(req.RequestTime))
	s.log.Debugf("Processing request '%d'", req.RequestID)
	s.log.Debug("Waiting for I2C busy pin to go low.")
	for {
		if s.busyPin.Read() == gpio.Low {
			s.log.Debugf("Waited %s for I2C busy pin to go low.", time.Since(startTime))
			s.log.Debug("I2C busy pin went low.")
			if err := s.busyPin.Out(gpio.High); err != nil {
				return Response{
					Err: dbus.NewError("org.cacophony.i2c.ErrorUsingBusyBusPin ", nil),
				}
			}
			break
		}
		if time.Since(startTime) > time.Duration(req.Timeout)*time.Millisecond {
			s.log.Debugf("Request '%d' timed out waiting for bus pin", req.RequestID)
			return Response{
				Err: dbus.NewError("org.cacophony.i2c.BusyTimeout", nil),
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	defer s.busyPin.In(gpio.Float, gpio.NoEdge)
	s.log.Debug("Driving pin high and locked the transaction.")

	read := make([]byte, req.ReadLen)
	retries := 2
	s.log.Debugf("Writing %v", req.Write)
	s.log.Debugf("Reading %d bytes", len(read))
	for i := 0; i <= retries; i++ {
		txStartTime := time.Now()
		err := s.bus.Tx(uint16(req.Address), req.Write, read)
		if err == nil {
			endTime := time.Now()
			s.log.Debugf("I2C Tx succeeded after %d retries", i)
			s.log.Debugf("Total request took (including queue time) %s", endTime.Sub(req.RequestTime))
			s.log.Debugf("I2C Tx took %s", endTime.Sub(txStartTime))
			s.log.Debugf("Response %v", read)
			return Response{
				Data: read,
			}
		}

		if i < retries {
			s.log.Debugf("I2C Tx failed, retrying %d more times: %s", retries-i, err)
			time.Sleep(20 * time.Millisecond)
		}
	}
	s.log.Errorf("I2C Tx failed. Address 0x%x, Write %v, ReadLen %d, ", req.Address, req.Write, req.ReadLen)
	return Response{
		Err: dbus.NewError("org.cacophony.i2c.ErrorUsingI2CBus", nil),
	}
}
