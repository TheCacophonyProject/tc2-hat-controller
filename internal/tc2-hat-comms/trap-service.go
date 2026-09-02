// DBus service for controlling the trap. This lets other services send requests,
// such as restarting the trap, while trap-control is running and has the serial port open.

package comms

import (
	"errors"
	"runtime"
	"strings"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
)

const (
	trapDbusName = "org.cacophony.trap"
	trapDbusPath = "/org/cacophony/trap"
)

type trapService struct {
	messenger *TrapMessenger
}

// startTrapService exports the trap service onto the system bus.
// Requests are sent to the trap through the given messenger, which serializes them
// with the messages being sent by the trap control loop.
func startTrapService(messenger *TrapMessenger) error {
	log.Info("Starting trap service")
	conn, err := dbus.SystemBus()
	if err != nil {
		return err
	}
	reply, err := conn.RequestName(trapDbusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return errors.New("name already taken")
	}

	s := &trapService{
		messenger: messenger,
	}
	if err := conn.Export(s, trapDbusPath, trapDbusName); err != nil {
		return err
	}
	return conn.Export(genTrapIntrospectable(s), trapDbusPath, "org.freedesktop.DBus.Introspectable")
}

// Restart restarts the trap.
func (s *trapService) Restart() *dbus.Error {
	log.Info("Restarting trap from DBus request")
	if err := s.messenger.Restart(); err != nil {
		log.Errorf("Failed to restart trap: %v", err)
		return trapDbusErr(err)
	}
	return nil
}

// Stop asks the trap to stop what it is doing, putting it into manual mode.
func (s *trapService) Stop() *dbus.Error {
	log.Info("Stopping trap from DBus request")
	if err := s.messenger.Stop(); err != nil {
		log.Errorf("Failed to stop trap: %v", err)
		return trapDbusErr(err)
	}
	return nil
}

// ReleaseSpool asks the trap to release the spool, putting it into manual mode.
func (s *trapService) ReleaseSpool() *dbus.Error {
	log.Info("Releasing spool from DBus request")
	if err := s.messenger.ReleaseSpool(); err != nil {
		log.Errorf("Failed to release spool: %v", err)
		return trapDbusErr(err)
	}
	return nil
}

// ResetSpool asks the trap to reset the spool, putting it into manual mode.
func (s *trapService) ResetSpool() *dbus.Error {
	log.Info("Resetting spool from DBus request")
	if err := s.messenger.ResetSpool(); err != nil {
		log.Errorf("Failed to reset spool: %v", err)
		return trapDbusErr(err)
	}
	return nil
}

// OpenDoor asks the trap to open one of its ratchet doors, putting it into manual mode.
func (s *trapService) OpenDoor(door int32) *dbus.Error {
	log.Infof("Opening door %d from DBus request", door)
	if err := s.messenger.OpenDoor(int(door)); err != nil {
		log.Errorf("Failed to open door %d: %v", door, err)
		return trapDbusErr(err)
	}
	return nil
}

// CloseDoor asks the trap to close one of its ratchet doors, putting it into manual mode.
func (s *trapService) CloseDoor(door int32) *dbus.Error {
	log.Infof("Closing door %d from DBus request", door)
	if err := s.messenger.CloseDoor(int(door)); err != nil {
		log.Errorf("Failed to close door %d: %v", door, err)
		return trapDbusErr(err)
	}
	return nil
}

func genTrapIntrospectable(v interface{}) introspect.Introspectable {
	node := &introspect.Node{
		Interfaces: []introspect.Interface{{
			Name:    trapDbusName,
			Methods: introspect.Methods(v),
		}},
	}
	return introspect.NewIntrospectable(node)
}

func trapDbusErr(err error) *dbus.Error {
	if err == nil {
		return nil
	}
	return &dbus.Error{
		Name: trapDbusName + "." + getTrapCallerName(),
		Body: []interface{}{err.Error()},
	}
}

func getTrapCallerName() string {
	fpcs := make([]uintptr, 1)
	n := runtime.Callers(3, fpcs)
	if n == 0 {
		return ""
	}
	caller := runtime.FuncForPC(fpcs[0] - 1)
	if caller == nil {
		return ""
	}
	funcNames := strings.Split(caller.Name(), ".")
	return funcNames[len(funcNames)-1]
}
