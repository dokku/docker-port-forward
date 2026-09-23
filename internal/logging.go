package internal

import "fmt"

// ValidateLogConfig checks a helper logging driver and its options the way
// `docker container create --log-driver/--log-opt` does: options are not
// allowed with the "none" driver.
func ValidateLogConfig(driver string, opts map[string]string) error {
	if driver == "none" && len(opts) > 0 {
		return fmt.Errorf("invalid logging opts for driver %s", driver)
	}
	return nil
}
