package comms

import (
	"testing"
	"time"

	"github.com/TheCacophonyProject/go-config"
	"github.com/stretchr/testify/assert"
)

func TestTrapState(t *testing.T) {
	now := time.Date(2026, 10, 22, 21, 30, 0, 0, time.Local)
	possum := func(ago time.Duration) sighting {
		return sighting{time: now.Add(-ago), animal: "possum", confidence: 90}
	}
	kiwi := func(ago time.Duration) sighting {
		return sighting{time: now.Add(-ago), animal: "kiwi", confidence: 40}
	}

	tests := []struct {
		name             string
		enabledByDefault bool
		protect          sighting
		trap             sighting
		wantEnabled      bool
		wantReason       string
		wantAnimal       string
	}{
		{
			name:             "default enabled, nothing seen",
			enabledByDefault: true,
			wantEnabled:      true,
			wantReason:       "No protect or trap species seen recently, trap is enabled by default",
		},
		{
			name:        "default disabled, nothing seen",
			wantEnabled: false,
			wantReason:  "No protect or trap species seen recently, trap is disabled by default",
		},
		{
			name:        "trap species seen recently",
			trap:        possum(10 * time.Second),
			wantEnabled: true,
			wantReason:  "Found an possum of confidence 90 that needs to be trapped (seen at 21:29:50)",
			wantAnimal:  "possum",
		},
		{
			name:             "protect species seen recently",
			enabledByDefault: true,
			protect:          kiwi(10 * time.Second),
			wantEnabled:      false,
			wantReason:       "Found an kiwi of confidence 40 that needs to be protected (seen at 21:29:50)",
			wantAnimal:       "kiwi",
		},
		{
			name:        "protect takes priority over trap",
			protect:     kiwi(10 * time.Second),
			trap:        possum(5 * time.Second),
			wantEnabled: false,
			wantReason:  "Found an kiwi of confidence 40 that needs to be protected (seen at 21:29:50)",
			wantAnimal:  "kiwi",
		},
		{
			name:        "trap species still in window after protect window ends",
			protect:     kiwi(2 * time.Minute),
			trap:        possum(90 * time.Second),
			wantEnabled: true,
			wantReason:  "Found an possum of confidence 90 that needs to be trapped (seen at 21:28:30)",
			wantAnimal:  "possum",
		},
		{
			name:        "trap window ended, back to default disabled",
			trap:        possum(10 * time.Minute),
			wantEnabled: false,
			wantReason:  "No protect or trap species seen recently, trap is disabled by default",
		},
		{
			name:             "protect window ended, back to default enabled",
			enabledByDefault: true,
			protect:          kiwi(2 * time.Minute),
			wantEnabled:      true,
			wantReason:       "No protect or trap species seen recently, trap is enabled by default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conf := &CommsConfig{Comms: config.Comms{
				TrapEnabledByDefault: tt.enabledByDefault,
				ProtectDuration:      time.Minute,
				TrapDuration:         2 * time.Minute,
			}}
			enabled, reason, trigger := trapState(conf, tt.protect, tt.trap, now)
			assert.Equal(t, tt.wantEnabled, enabled)
			assert.Equal(t, tt.wantReason, reason)
			if tt.wantAnimal == "" {
				assert.Nil(t, trigger)
			} else if assert.NotNil(t, trigger) {
				assert.Equal(t, tt.wantAnimal, trigger.animal)
			}
		})
	}
}
