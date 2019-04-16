import "time"

type dbReady func(Instance) bool

func (s *SDK) waitForDBStatus(id string, ready dbReady) error {
	sleep := defaultSleep
	for {
		time.Sleep(time.Millisecond * time.Duration(sleep))
		i, err := s.Describe(id)
		if err != nil {
			return err
		}

		if ready(i) {
			break
		}

		sleep = sleep * drift
	}
	return nil
}
