package tuner

import (
	"context"
	"strings"
	"testing"
)

func TestDemoIsStablePerChannel(t *testing.T) {
	a, _ := Demo{}.Input(context.Background(), "7.1")
	b, _ := Demo{}.Input(context.Background(), "7.1")
	if strings.Join(a.Args, " ") != strings.Join(b.Args, " ") {
		t.Error("demo pattern changed between tunes of the same channel")
	}
}
