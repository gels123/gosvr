package main

import (
	"time"
)

type Student_ struct {
	name string
	age  int
}

func logf1() {
	//log.Init("../../log", "game", log.DEBUG_LEVEL, log.DEBUG_LEVEL, 10000, 1000)
	//s := &Student_{"yyyyy", 100}
	//log.Debug("hahaha %v, %v", 2, s)
	//log.Error("hahaha %v, %v", 2, s)
	//log.Warn("hahaha %v, %v", 2, s)
	////log.Fatal("hahaha %v, %v", 2, s)
}

func logf2() {
	//zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	//log.Print("hello world")

	//logger, _ := zap.NewProduction()
	//defer logger.Sync() // flushes buffer, if any
	//sugar := logger.Sugar()
	//sugar.Infow("failed to fetch URL",
	//	// Structured context as loosely typed key-value pairs.
	//	"url", "wwwwwffff",
	//	"attempt", 3,
	//	"backoff", time.Second,
	//)
	//sugar.Infof("Failed to fetch URL: %s", "wwwwwffff")

	SetSource("gwlog_test")
	SetOutput([]string{"stderr", "gwlog_test.log"})
	SetLevel(DebugLevel)

	if lv := ParseLevel("debug"); lv != DebugLevel {
		t.Fail()
	}
	if lv := ParseLevel("info"); lv != InfoLevel {
		t.Fail()
	}
	if lv := ParseLevel("warn"); lv != WarnLevel {
		t.Fail()
	}
	if lv := ParseLevel("error"); lv != ErrorLevel {
		t.Fail()
	}
	if lv := ParseLevel("panic"); lv != PanicLevel {
		t.Fail()
	}
	if lv := ParseLevel("fatal"); lv != FatalLevel {
		t.Fail()
	}

	Debugf("this is a debug %d", 1)
	SetLevel(InfoLevel)
	Debugf("SHOULD NOT SEE THIS!")
	Infof("this is an info %d", 2)
	Warnf("this is a warning %d", 3)
	TraceError("this is a trace error %d", 4)
	func() {
		defer func() {
			_ = recover()
		}()
		Panicf("this is a panic %d", 4)
	}()

	func() {
		defer func() {
			_ = recover()
		}()
		//Fatalf("this is a fatal %d", 5)
	}()
}

func main() {
	logf2()
	for {
		time.Sleep(2 * time.Second)
	}
}
