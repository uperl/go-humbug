package protocol

import (
	"io"
	"os"
)

// stderr receives process-level diagnostics only; stdout carries protocol traffic.
var stderr io.Writer = os.Stderr
