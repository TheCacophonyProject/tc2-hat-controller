package i2c

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/TheCacophonyProject/event-reporter/v3/eventclient"
	"github.com/godbus/dbus"
	"github.com/godbus/dbus/introspect"
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
	requestCount int
}

var waitingForBinToBeAvailable bool
var mu sync.Mutex

func startService() error {
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

func timeBusyPinBusyDuration(busyPin gpio.PinIO, startTime time.Time) {
	log.Info("Checking how long I2C is busy for.")
	for {
		if busyPin.Read() == gpio.Low {
			mu.Lock()
			waitingForBinToBeAvailable = false
			mu.Unlock()
			waitTime := time.Since(startTime)
			log.Infof("Waited %s for I2C busy pin to go low.", waitTime)
			err := eventclient.AddEvent(eventclient.Event{
				Timestamp: time.Now(),
				Type:      "i2cBusyPinTimeout",
				Details:   map[string]interface{}{"Seconds I2C was busy for": waitTime.Seconds()},
			})
			if err != nil {
				log.Errorf("Error adding event: %v", err)
			}
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
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

/*
// I2C example to read a register from the ATtiny1616.
// Address: 		0x25
// Register:	 	0x00
// CRC: 				0xcc,0x9c
// Read length: 3
// Timeout: 		100ms
dbus-send --system --print-reply --dest=org.cacophony.i2c /org/cacophony/i2c org.cacophony.i2c.Tx \
byte:0x25 \
array:byte:0x00,0xcc,0x9c \
int32:3 \
int32:100
*/

// Tx sends a transaction to the I2C device, used for reading and writing to registers.
// If reading/writing to the ATtiny remember the CRC bytes.
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
	log.Debugf("Adding request '%d' to the queue", requestID)
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
	log.Debugf("Waited %s for request to be processed.", startTime.Sub(req.RequestTime))
	log.Debugf("Processing request '%d'", req.RequestID)
	log.Debug("Waiting for I2C busy pin to go low.")
	for {
		if s.busyPin.Read() == gpio.Low {
			log.Debugf("Waited %s for I2C busy pin to go low.", time.Since(startTime))
			log.Debug("I2C busy pin went low.")
			if err := s.busyPin.Out(gpio.High); err != nil {
				return Response{
					Err: dbus.NewError("org.cacophony.i2c.ErrorUsingBusyBusPin ", nil),
				}
			}
			break
		}
		if time.Since(startTime) > time.Duration(req.Timeout)*time.Millisecond {

			mu.Lock()
			if !waitingForBinToBeAvailable {
				waitingForBinToBeAvailable = true
				go timeBusyPinBusyDuration(s.busyPin, startTime)
			}
			mu.Unlock()

			log.Infof("Request '%d' timed out waiting for bus pin", req.RequestID)
			return Response{
				Err: dbus.NewError("org.cacophony.i2c.BusyTimeout", nil),
			}
		}
		time.Sleep(2 * time.Millisecond)
	}

	defer s.busyPin.In(gpio.Float, gpio.NoEdge)
	log.Debug("Driving pin high and locked the transaction.")

	read := make([]byte, req.ReadLen)
	retries := 2
	log.Debugf("Writing %v", req.Write)
	log.Debugf("Reading %d bytes", len(read))
	for i := 0; i <= retries; i++ {
		txStartTime := time.Now()
		err := s.bus.Tx(uint16(req.Address), req.Write, read)
		if err == nil {
			endTime := time.Now()
			log.Debugf("I2C Tx succeeded after %d retries", i)
			log.Debugf("Total request took (including queue time) %s", endTime.Sub(req.RequestTime))
			log.Debugf("I2C Tx took %s", endTime.Sub(txStartTime))
			log.Debugf("Response %v", read)
			return Response{
				Data: read,
			}
		}

		if i < retries {
			log.Debugf("I2C Tx failed, retrying %d more times: %s", retries-i, err)
			time.Sleep(20 * time.Millisecond)
		}
	}
	log.Errorf("I2C Tx failed. Address 0x%x, Write %v, ReadLen %d, ", req.Address, req.Write, req.ReadLen)
	return Response{
		Err: dbus.NewError("org.cacophony.i2c.ErrorUsingI2CBus", nil),
	}
}
