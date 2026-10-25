package garmin

// Response is a successful (2xx) Garmin reply.
type Response struct {
	Status      int
	Body        []byte
	ContentType string
}
