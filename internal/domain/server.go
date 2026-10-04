package domain

// Server is the server's identity. It does not change across restarts.
type Server struct {
	ID   ID
	Name string
}
