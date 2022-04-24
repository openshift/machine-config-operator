package main

import (
	"fmt"

	log "github.com/InVisionApp/go-logger"
	"k8s.io/klog/v2"
)

type logger struct {
	keysAndValues []interface{}
}

func (k *logger) Debug(msg ...interface{}) {
	klog.V(4).InfoSDepth(1, fmt.Sprint(msg...), k.keysAndValues...)
}

func (k *logger) Info(msg ...interface{}) {
	klog.V(0).InfoSDepth(1, fmt.Sprint(msg...), k.keysAndValues...)
}

func (k *logger) Warn(msg ...interface{}) {
	klog.V(1).InfoSDepth(1, fmt.Sprint(msg...), k.keysAndValues...)
}

func (k *logger) Error(msg ...interface{}) {
	klog.ErrorSDepth(1, nil, fmt.Sprint(msg...), k.keysAndValues...)
}

func (k *logger) Debugln(msg ...interface{}) {
	klog.V(4).InfoSDepth(1, fmt.Sprintln(msg...), k.keysAndValues...)
}

func (k *logger) Infoln(msg ...interface{}) {
	klog.V(0).InfoSDepth(1, fmt.Sprintln(msg...), k.keysAndValues...)
}

func (k *logger) Warnln(msg ...interface{}) {
	klog.V(1).InfoSDepth(1, fmt.Sprintln(msg...), k.keysAndValues...)
}

func (k *logger) Errorln(msg ...interface{}) {
	klog.ErrorSDepth(1, nil, fmt.Sprintln(msg...), k.keysAndValues...)
}

func (k *logger) Debugf(format string, args ...interface{}) {
	klog.V(4).InfoSDepth(1, fmt.Sprintf(format, args...), k.keysAndValues...)
}

func (k *logger) Infof(format string, args ...interface{}) {
	klog.V(0).InfoSDepth(1, fmt.Sprintf(format, args...), k.keysAndValues...)
}

func (k *logger) Warnf(format string, args ...interface{}) {
	klog.V(1).InfoSDepth(1, fmt.Sprintf(format, args...), k.keysAndValues...)
}

func (k *logger) Errorf(format string, args ...interface{}) {
	klog.ErrorSDepth(1, nil, fmt.Sprintf(format, args...), k.keysAndValues...)
}

func (k *logger) WithFields(fields log.Fields) log.Logger {
	l := &logger{}
	for k, v := range fields {
		l.keysAndValues = append(l.keysAndValues, k, v)
	}
	return l
}
