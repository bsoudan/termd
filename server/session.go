package main

// Session groups regions under a named session.
// All fields are protected by Server.mu.
type Session struct {
	name    string
	regions map[string]*Region // region ID → *Region
}

func NewSession(name string) *Session {
	return &Session{
		name:    name,
		regions: make(map[string]*Region),
	}
}
