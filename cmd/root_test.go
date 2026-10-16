package cmd

import (
	"testing"

	"github.com/machinae/safewebtool/config"
)

func TestMaxOutput_Negative(t *testing.T) {
	c := &config.Config{MaxOutput: -1}
	if err := c.Validate(); err == nil {
		t.Error("expected validation error for negative max_output")
	}
}
