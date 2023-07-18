package state

/*
import (
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/util/workqueue"
)

type UpgradeStateController struct {
	syncHandler func(pool string)
	config      StateControllerConfig
	queue       workqueue.RateLimitingInterface
}

func newUpgradeStateController(
	ctrlConfig StateControllerConfig, watcher watch.Interface,
) *UpgradeStateController {
	ctrl := &UpgradeStateController{
		config: ctrlConfig,
		queue:  workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "machinestatecontroller-upgradestatecontroller"),
	}

	ctrl.syncHandler = ctrl.syncUpgradeProgression

	// need to think of listers/informers I am going to need for this

	return ctrl
}

// Run

func (ctrl *UpgradeStateController) Run() error {

	return nil
}

// worker
func (ctrl *UpgradeStateController) worker() {

}

func (ctrl *UpgradeStateController) processNextWorkItem() bool {
	key, quit := ctrl.queue.Get()
	if quit {
		return false
	}
	defer ctrl.queue.Done(key)
	ctrl.syncHandler(key.(string))
	return true
}

func (ctrl *UpgradeStateController) syncPoolHealthProgression(pool string) {

}

// processNextWorkItem

// syncHandler
*/
