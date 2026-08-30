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
