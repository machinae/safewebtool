package config

import (
	"testing"
	"time"
)

func TestValidateNegativeTimeout(t *testing.T) {
	c := &Config{Timeout: -1 * time.Second}
	if err := c.Validate(); err == nil {
		t.Error("expected error for negative timeout")
	}
}

func TestValidateNegativeMaxOutput(t *testing.T) {
	c := &Config{MaxOutput: -1}
	if err := c.Validate(); err == nil {
		t.Error("expected error for negative max_output")
	}
}

func TestValidateEmptyChromeDataDir(t *testing.T) {
	c := &Config{ChromeDataDir: "", Timeout: 30 * time.Second}
	if err := c.Validate(); err != nil {
		t.Errorf("empty chrome_data_dir should be valid: %v", err)
	}
}
