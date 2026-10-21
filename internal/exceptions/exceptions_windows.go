package exceptions

import "syscall"

// init adds the Winsock errors for an aborted, reset or unconnected socket to closedErrors.
func init() {
	closedErrors = append(closedErrors,
		syscall.Errno(10053), // WSAECONNABORTED
		syscall.Errno(10054), // WSAECONNRESET
		syscall.Errno(10057), // WSAENOTCONN
	)
}
