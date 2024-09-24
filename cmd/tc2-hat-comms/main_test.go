package main

import (
	"testing"

	"github.com/TheCacophonyProject/tc2-hat-controller/tracks"
	"github.com/stretchr/testify/assert"
)

func TestClassificationTrigger(t *testing.T) {
	trapSpecies := tracks.Species{
		"possum":   70,
		"cat":      70,
		"hedgehog": 70,
	}

	protectSpecies := tracks.Species{
		"bird": 50,
		"kiwi": 30,
	}

	// Maybe a kiwi, should not trigger
	assert.False(t, tracks.Species{
		"possum": 90,
		"kiwi":   40,
	}.ShouldTrigger(trapSpecies, protectSpecies))

	// Empty classifications, should not trigger
	assert.False(t, tracks.Species{}.ShouldTrigger(trapSpecies, protectSpecies))

	// Nothing to trap, should not trigger
	assert.False(t, tracks.Species{
		"possum": 50,
	}.ShouldTrigger(trapSpecies, protectSpecies))

	// Possum, should trigger
	assert.True(t, tracks.Species{
		"possum": 90,
		"kiwi":   20,
	}.ShouldTrigger(trapSpecies, protectSpecies))
}
