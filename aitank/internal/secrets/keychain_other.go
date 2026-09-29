//go:build !darwin

package secrets

func platformStore() Store { return unsupported{} }

type unsupported struct{}

func (unsupported) Get(string) (string, error)       { return "", ErrUnsupported }
func (unsupported) Set(string, string, string) error { return ErrUnsupported }
func (unsupported) Delete(string) error              { return nil }
func (unsupported) Check() error                     { return ErrUnsupported }
