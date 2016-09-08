package dumy

import (
	log "github.com/Sirupsen/logrus"
	"time"
)

const (
	ProtoName = "DUMY"
)

type Analyzer struct {
}

func (a *Analyzer) Init() {
	log.Info("Dumy Analyzer: dumy init.")
}

func (a *Analyzer) Proto() string {
	return ProtoName
}

func (a *Analyzer) HandleEstb(timestamp time.Time) {
	log.Info("Dumy Analyzer: dumy HandleEstb.")
}

func (a *Analyzer) HandleData(payload []byte, fromClient bool, timestamp time.Time) (parseBytes int, sessionDone bool) {
	log.Infof("Dumy Analyzer: from client=%t get %d bytes data %s...",
		fromClient, len(payload), string((payload)[1:32]))
	return len(payload), false
}

func (a *Analyzer) HandleReset(fromClient bool, timestamp time.Time) (sessionDone bool) {
	log.Infof("Dumy Analyzer: from client=%t get rest.", fromClient)
	return false
}

func (a *Analyzer) HandleFin(fromClient bool, timestamp time.Time) (sessionDone bool) {
	log.Infof("Dumy Analyzer: from client=%t get fin.", fromClient)
	return false
}