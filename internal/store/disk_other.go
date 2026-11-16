//go:build !unix

package store

func diskSpace(string) (free, total int64) { return 0, 0 }
