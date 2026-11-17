package probe

import "time"

// Checks in tests make their own connection first and then not for a whole
// period, so that what a test sees does not depend on chance.
func init() { freshJitter = func(time.Duration) time.Duration { return 0 } }
