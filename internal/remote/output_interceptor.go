package remote

import (
	"errors"
	"os"
)

/*
The OutputInterceptor is used by the ForwardingReporter to
intercept and capture all stdin and stderr output during a test run.
*/
type OutputInterceptor interface {
	StartInterceptingOutput() error
	StopInterceptingAndReturnOutput() (string, error)
	StreamTo(*os.File)
}

func NewOutputInterceptor() OutputInterceptor {
	return &outputInterceptor{}
}

type outputInterceptor struct {
	intercepting bool
}

func (interceptor *outputInterceptor) StartInterceptingOutput() error {
	if interceptor.intercepting {
		return errors.New("Already intercepting output!")
	}
	interceptor.intercepting = true

	return nil
}

func (interceptor *outputInterceptor) StopInterceptingAndReturnOutput() (string, error) {
	interceptor.intercepting = false
	return "", nil
}

func (interceptor *outputInterceptor) StreamTo(*os.File) {}
