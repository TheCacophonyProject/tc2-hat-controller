package comms

import (
	"errors"
	"testing"
	"time"

	"periph.io/x/conn/v3/gpio"
)

func TestSendATCommandIsolatesUART(t *testing.T) {
	origIsolate := isolateESLUARTAfterAT
	origSend := eslSerialSend
	origRecv := eslSerialSendReceiveUntil
	origSettle := atPostWakeSettle
	t.Cleanup(func() {
		isolateESLUARTAfterAT = origIsolate
		eslSerialSend = origSend
		eslSerialSendReceiveUntil = origRecv
		atPostWakeSettle = origSettle
	})

	atPostWakeSettle = 0

	var isolateCalls int
	isolateESLUARTAfterAT = func() error {
		isolateCalls++
		return nil
	}
	eslSerialSend = func(retries int, mul0, mul1 gpio.Level, wait time.Duration, data []byte, baud int) error {
		return nil
	}
	eslSerialSendReceiveUntil = func(retries int, mul0, mul1 gpio.Level, wait time.Duration, data []byte, baud int, timeout time.Duration, endMarkers ...[]byte) ([]byte, error) {
		return []byte("O^K\r\n"), nil
	}

	if _, err := sendATCommand("AT+XCMD=m05", 4800); err != nil {
		t.Fatalf("sendATCommand: %v", err)
	}
	if isolateCalls != 1 {
		t.Fatalf("isolate calls = %d, want 1 after successful AT command", isolateCalls)
	}

	isolateCalls = 0
	eslSerialSend = func(retries int, mul0, mul1 gpio.Level, wait time.Duration, data []byte, baud int) error {
		return errors.New("serial unavailable")
	}
	if _, err := sendATCommand("AT+XCMD=m05", 4800); err == nil {
		t.Fatal("expected serial error")
	}
	if isolateCalls != 1 {
		t.Fatalf("isolate calls = %d, want 1 after failed wake", isolateCalls)
	}

	isolateCalls = 0
	if _, err := sendATCommand("AT+XCMD=m05", 0); err != nil {
		t.Fatalf("test-mode sendATCommand: %v", err)
	}
	if isolateCalls != 0 {
		t.Fatalf("isolate calls = %d, want 0 in baud-rate 0 test mode", isolateCalls)
	}
}
