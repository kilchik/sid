package main

type errorNotAllowed struct {
}

func (ena errorNotAllowed) Error() string {
	return "not allowed"
}
