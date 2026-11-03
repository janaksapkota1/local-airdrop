//go:build !unix

package transfer

func freeBytes(string) int64 { return -1 }
