package supervisor

import (
	"github.com/FusionCube18712/claude-auto-resume/internal/detect"
	"github.com/FusionCube18712/claude-auto-resume/internal/profiles"
)

// newDetector builds the output detector from a profile's limit patterns.
func newDetector(p profiles.Profile) limitFeeder {
	return detect.New(p.LimitPatterns, 32<<10)
}

// spawn starts the child (PTY or pipe) and, for PTY runs, begins forwarding
// terminal resize events to it. A test may override stream creation via
// Config.newStream.
func (s *session) spawn(argv []string) (stream, error) {
	if s.cfg.newStream != nil {
		st, err := s.cfg.newStream(argv)
		if err != nil {
			return nil, err
		}
		s.reporter.OnStart(argv, s.cfg.Mode)
		return st, nil
	}
	var (
		st  stream
		err error
	)
	if s.cfg.UsePTY {
		st, err = newPTYStream(argv, s.cfg.Env, s.cfg.Dir)
	} else {
		st, err = newPipeStream(argv, s.cfg.Env, s.cfg.Dir)
	}
	if err != nil {
		return nil, err
	}
	if s.cfg.UsePTY {
		s.stopResize = startResize(st)
	}
	s.reporter.OnStart(argv, s.cfg.Mode)
	return st, nil
}
