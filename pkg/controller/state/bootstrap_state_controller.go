package state

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/fsnotify/fsnotify"
	v1 "github.com/openshift/api/machineconfiguration/v1"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
)

// I don't think we need euqueue, workers, or any of that. Just needs to run and listen

type BootstrapStateController struct {
	config                            StateControllerConfig
	queue                             workqueue.RateLimitingInterface
	enqueueBoostrapMachineConfigState func(*v1.MachineConfigState)
	stopCh                            chan struct{}
	wg                                sync.WaitGroup
}

func newBootstrapStateController(
	ctrlConfig StateControllerConfig,
) *BootstrapStateController {
	ctrl := &BootstrapStateController{
		config: ctrlConfig,
	}

	// need to think of listers/informers I am going to need for this

	return ctrl
}

func (ctrl *BootstrapStateController) Stop() {
	ctrl.stopCh <- struct{}{}
	ctrl.wg.Wait()
	klog.Info("Bootstrap state controller has shut down")
}

func (ctrl *BootstrapStateController) Run(workers int, stopCh <-chan struct{}) error {
	//if !cache.WaitForCacheSync(ctx.Done(), ) {
	//	return
	//}

	//for i := 0; i < workers; i++ {
	//	go wait.Until(ctrl.worker, time.Second, stopCh)
	//}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	// watch for file changes. we can call stop once the MSC file served into the initial cluster is created
	go func() error {
		//	defer c.wg.Done()
		for {
			select {
			case event := <-watcher.Events:
				// Our watcher is reporting an event that we should look at.
				if event.Name == "SOME_ARBITRARY_FILE" { // this is the MSC file, close everything
					<-stopCh
					return nil
				}
				f, err := os.ReadFile(event.Name)
				newMS := v1.MachineConfigState{}
				if err = json.Unmarshal(f, &newMS); err != nil {
					return err
				}
				switch newMS.Kind {
				//case string(v1.MCCBootstrapProgression):
				// erase and re-write controller state
				//dcase string(v1.MCSBootstrapProgression):
				// erase and re-write server state
				}
			case err := <-watcher.Errors:
				// Send fsnotify errors directly to the error channel.
				return fmt.Errorf("fsnotify error: %w", err)
			case <-ctrl.stopCh:
				// We received a stop signal, shutdown our watcher.
				watcher.Close()
				return nil
			}
		}
	}()

	klog.Info("Bootstrap MSC started, gathering data")

	// might not need any of the enqueue stuff honestly.
	// read from somewhere you know mcc, mcs are writing data to in the form of a MachineConfigState or some sort of update
	// make sure you keep track of it and at the end, write it to disk
	// machine-state-controller --subcontrollers=bootstrap

	// read dir/file repeatedly
	// sync changes

	return nil
}

// we need to basically make this a stripped down
// version of the regular build controller
// where the queue is full of states that get enqueued differently than
// the API i'd assume
