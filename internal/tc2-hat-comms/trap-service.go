// DBus service for controlling the trap. This lets other services send requests,
// such as restarting the trap, while trap-control is running and has the serial port open.

package comms

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

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

// trapServiceConn is the bus the trap service is on, once it has started, for sending
// signals from messages the trap sends on its own.
var trapServiceConn atomic.Pointer[dbus.Conn]

// What tc2-hat-comms is doing to connect to the trap, before the trap's own status can be
// used. While connecting, the status is {"Connection": <stage>}, and requests to the trap
// are refused.
const (
	trapInitializing     = "INITIALIZING"      // The trap service has started, but not yet the PINGs
	trapPinging          = "PINGING"           // Waiting for the trap to answer a PING
	trapCheckingSoftware = "CHECKING_SOFTWARE" // Checking the trap's software is up to date
	trapUpdatingSoftware = "UPDATING_SOFTWARE" // Uploading the trap's software
	trapCheckingConfig   = "CHECKING_CONFIG"   // Checking the trap's config is up to date
	trapUpdatingConfig   = "UPDATING_CONFIG"   // Uploading the trap's config
	trapRestarting       = "RESTARTING"        // Waiting for the trap to restart after an update
)

var (
	trapStatusMu  sync.Mutex
	trapStatus    = trapInitializing
	trapConnected = false
)

// setStatus sets the connection stage and, if it has changed, sends it as a
// StatusChanged signal.
func setStatus(stage string) {
	trapStatusMu.Lock()
	defer trapStatusMu.Unlock()
	if trapStatus == stage {
		return
	}
	trapStatus = stage
	emitStatus(connectionStatusJSON(stage))
}

// connectionStatusJSON returns the status for a connection stage, as JSON.
func connectionStatusJSON(stage string) string {
	status, _ := json.Marshal(struct{ Connection string }{stage})
	return string(status)
}

// trapIsConnected marks the trap as connected, so its own status is used from now on, and
// sends that status, as the page would otherwise keep showing the last connection stage.
func trapIsConnected(messenger *TrapMessenger) {
	trapStatusMu.Lock()
	trapConnected = true
	trapStatusMu.Unlock()

	status, err := messenger.Status()
	if err != nil {
		log.Warnf("Failed to get the trap's status: %v", err)
		status = "{}"
	}
	emitStatus(status)
}

// isTrapConnected returns whether the trap has finished connecting.
func isTrapConnected() bool {
	trapStatusMu.Lock()
	defer trapStatusMu.Unlock()
	return trapConnected
}

// errIfConnecting refuses a request while still connecting to the trap.
func errIfConnecting() error {
	trapStatusMu.Lock()
	defer trapStatusMu.Unlock()
	if !trapConnected {
		return fmt.Errorf("the camera is still connecting to the trap (%s)", strings.ToLower(trapStatus))
	}
	return nil
}

// startTrapService exports the trap service onto the system bus. It is started before
// the trap has answered, so the connection stages can be seen (see setStatus).
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
	if err := conn.Export(genTrapIntrospectable(s), trapDbusPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return err
	}
	trapServiceConn.Store(conn)
	return nil
}

func emitStatus(status string) {
	conn := trapServiceConn.Load()
	if conn == nil {
		return
	}
	if err := conn.Emit(trapDbusPath, trapDbusName+".StatusChanged", status); err != nil {
		log.Errorf("Failed to send the trap's status over DBus: %v", err)
	}
}

// Status returns what the trap is doing, as JSON (see TrapMessenger.Status), or while
// still connecting to the trap, the connection stage (see setStatus).
// It isn't logged, as it is polled while the trap page is open.
func (s *trapService) Status() (string, *dbus.Error) {
	trapStatusMu.Lock()
	state := trapStatus
	connected := trapConnected
	trapStatusMu.Unlock()

	if !connected {
		return connectionStatusJSON(state), nil
	}

	status, err := s.messenger.Status()
	if err != nil {
		return "", trapDbusErr(err)
	}
	return status, nil
}

// Restart restarts the trap.
func (s *trapService) Restart() *dbus.Error {
	if err := errIfConnecting(); err != nil {
		return trapDbusErr(err)
	}
	log.Info("Restarting trap from DBus request")
	if err := s.messenger.Restart(); err != nil {
		log.Errorf("Failed to restart trap: %v", err)
		return trapDbusErr(err)
	}
	return nil
}

// Stop asks the trap to stop what it is doing, putting it into manual mode.
func (s *trapService) Stop() *dbus.Error {
	if err := errIfConnecting(); err != nil {
		return trapDbusErr(err)
	}
	log.Info("Stopping trap from DBus request")
	if err := s.messenger.Stop(); err != nil {
		log.Errorf("Failed to stop trap: %v", err)
		return trapDbusErr(err)
	}
	return nil
}

// ReleaseSpool asks the trap to release the spool, putting it into manual mode.
func (s *trapService) ReleaseSpool() *dbus.Error {
	if err := errIfConnecting(); err != nil {
		return trapDbusErr(err)
	}
	log.Info("Releasing spool from DBus request")
	if err := s.messenger.ReleaseSpool(); err != nil {
		log.Errorf("Failed to release spool: %v", err)
		return trapDbusErr(err)
	}
	return nil
}

// ResetSpool asks the trap to reset the spool, putting it into manual mode.
func (s *trapService) ResetSpool() *dbus.Error {
	if err := errIfConnecting(); err != nil {
		return trapDbusErr(err)
	}
	log.Info("Resetting spool from DBus request")
	if err := s.messenger.ResetSpool(); err != nil {
		log.Errorf("Failed to reset spool: %v", err)
		return trapDbusErr(err)
	}
	return nil
}

// OpenDoor asks the trap to open one of its ratchet doors, putting it into manual mode.
func (s *trapService) OpenDoor(door int32) *dbus.Error {
	if err := errIfConnecting(); err != nil {
		return trapDbusErr(err)
	}
	log.Infof("Opening door %d from DBus request", door)
	if err := s.messenger.OpenDoor(int(door)); err != nil {
		log.Errorf("Failed to open door %d: %v", door, err)
		return trapDbusErr(err)
	}
	return nil
}

// CloseDoor asks the trap to close one of its ratchet doors, putting it into manual mode.
func (s *trapService) CloseDoor(door int32) *dbus.Error {
	if err := errIfConnecting(); err != nil {
		return trapDbusErr(err)
	}
	log.Infof("Closing door %d from DBus request", door)
	if err := s.messenger.CloseDoor(int(door)); err != nil {
		log.Errorf("Failed to close door %d: %v", door, err)
		return trapDbusErr(err)
	}
	return nil
}

// CageHold asks the trap to keep holding the animal in its cage for longer.
func (s *trapService) CageHold() *dbus.Error {
	if err := errIfConnecting(); err != nil {
		return trapDbusErr(err)
	}
	log.Info("Holding the animal in the cage from DBus request")
	if err := s.messenger.CageHold(); err != nil {
		log.Errorf("Failed to hold the animal in the cage: %v", err)
		return trapDbusErr(err)
	}
	return nil
}

// CageRelease asks the trap to let the animal out of its cage.
func (s *trapService) CageRelease() *dbus.Error {
	if err := errIfConnecting(); err != nil {
		return trapDbusErr(err)
	}
	log.Info("Releasing the animal from the cage from DBus request")
	if err := s.messenger.CageRelease(); err != nil {
		log.Errorf("Failed to release the animal from the cage: %v", err)
		return trapDbusErr(err)
	}
	return nil
}

// CageKill asks the trap to open its cage to the kill mechanism.
func (s *trapService) CageKill() *dbus.Error {
	if err := errIfConnecting(); err != nil {
		return trapDbusErr(err)
	}
	log.Info("Opening the cage to the kill mechanism from DBus request")
	if err := s.messenger.CageKill(); err != nil {
		log.Errorf("Failed to open the cage to the kill mechanism: %v", err)
		return trapDbusErr(err)
	}
	return nil
}

// CageWait asks the trap to wait longer for the animal to leave its cage.
func (s *trapService) CageWait() *dbus.Error {
	if err := errIfConnecting(); err != nil {
		return trapDbusErr(err)
	}
	log.Info("Waiting longer for the animal to leave the cage from DBus request")
	if err := s.messenger.CageWait(); err != nil {
		log.Errorf("Failed to wait longer for the animal to leave the cage: %v", err)
		return trapDbusErr(err)
	}
	return nil
}

// CageEscaped tells the trap the animal has left its cage.
func (s *trapService) CageEscaped() *dbus.Error {
	if err := errIfConnecting(); err != nil {
		return trapDbusErr(err)
	}
	log.Info("Marking the animal in the cage as escaped from DBus request")
	if err := s.messenger.CageEscaped(); err != nil {
		log.Errorf("Failed to mark the animal in the cage as escaped: %v", err)
		return trapDbusErr(err)
	}
	return nil
}

// CageKilled tells the trap the animal in its cage has been killed.
func (s *trapService) CageKilled() *dbus.Error {
	if err := errIfConnecting(); err != nil {
		return trapDbusErr(err)
	}
	log.Info("Marking the animal in the cage as killed from DBus request")
	if err := s.messenger.CageKilled(); err != nil {
		log.Errorf("Failed to mark the animal in the cage as killed: %v", err)
		return trapDbusErr(err)
	}
	return nil
}

func genTrapIntrospectable(v interface{}) introspect.Introspectable {
	node := &introspect.Node{
		Interfaces: []introspect.Interface{{
			Name:    trapDbusName,
			Methods: introspect.Methods(v),
			Signals: []introspect.Signal{{
				Name: "StatusChanged",
				Args: []introspect.Arg{{Name: "status", Type: "s"}},
			}},
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
