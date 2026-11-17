//go:build !unix

package selfstat

import "time"

func processCPU() time.Duration { return 0 }
