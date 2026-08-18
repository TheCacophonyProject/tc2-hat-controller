package serialhelper

import (
	"errors"
	"reflect"
	"testing"
)

func TestIsolateESLUARTCommands(t *testing.T) {
	var got [][]string
	orig := raspiGPIO
	raspiGPIO = func(args ...string) ([]byte, error) {
		cp := append([]string{}, args...)
		got = append(got, cp)
		return nil, nil
	}
	t.Cleanup(func() { raspiGPIO = orig })

	if err := IsolateESLUART(); err != nil {
		t.Fatalf("IsolateESLUART: %v", err)
	}

	want := [][]string{
		{"set", "6", "ip", "pu"},
		{"set", "12", "ip", "pd"},
		{"set", "14", "ip"},
		{"set", "15", "ip"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("raspi-gpio calls = %#v, want %#v", got, want)
	}
}

func TestIsolateESLUARTPropagatesError(t *testing.T) {
	orig := raspiGPIO
	raspiGPIO = func(args ...string) ([]byte, error) {
		return []byte("boom"), errors.New("exit 1")
	}
	t.Cleanup(func() { raspiGPIO = orig })

	err := IsolateESLUART()
	if err == nil {
		t.Fatal("expected error")
	}
}
