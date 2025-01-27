package build

import (
	"fmt"
	"sync/atomic"
	"time"

	"k8s.io/klog"
	"k8s.io/kubernetes/pkg/api/legacyscheme"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/wait"
	watchapi "k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	buildv1 "github.com/openshift/api/build/v1"
	imagev1 "github.com/openshift/api/image/v1"
	buildv1clienttyped "github.com/openshift/client-go/build/clientset/versioned/typed/build/v1"
	imagev1clienttyped "github.com/openshift/client-go/image/clientset/versioned/typed/image/v1"
	buildutil "github.com/openshift/openshift-controller-manager/pkg/build/buildutil"
	testutil "github.com/openshift/origin/test/util"
)

var (
	//TODO: Make these externally configurable

	// BuildControllerTestWait is the time that RunBuildControllerTest waits
	// for any other changes to happen when testing whether only a single build got processed
	BuildControllerTestWait = 10 * time.Second

	// BuildControllerTestTransitionTimeout is the time RunBuildControllerPodSyncTest waits
	// for a build trasition to occur after the pod's status has been updated
	BuildControllerTestTransitionTimeout = 60 * time.Second

	// BuildControllersWatchTimeout is used by all tests to wait for watch events. In case where only
	// a single watch event is expected, the test will fail after the timeout.
	// The value is 6 minutes to allow for a resync to occur, which allows for necessarily
	// reconciliation to occur in tests where events occur in a non-deterministic order.
	BuildControllersWatchTimeout = 360 * time.Second
)

type testingT interface {
	Fail()
	Error(args ...interface{})
	Errorf(format string, args ...interface{})
	FailNow()
	Fatal(args ...interface{})
	Fatalf(format string, args ...interface{})
	Log(args ...interface{})
	Logf(format string, args ...interface{})
	Failed() bool
	Parallel()
	Skip(args ...interface{})
	Skipf(format string, args ...interface{})
	SkipNow()
	Skipped() bool
}

func mockBuild() *buildv1.Build {
	return &buildv1.Build{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "mock-build",
			Labels: map[string]string{
				"label1":                    "value1",
				"label2":                    "value2",
				buildv1.BuildConfigLabel:    "mock-build-config",
				buildv1.BuildRunPolicyLabel: string(buildv1.BuildRunPolicyParallel),
			},
		},
		Spec: buildv1.BuildSpec{
			CommonSpec: buildv1.CommonSpec{
				Source: buildv1.BuildSource{
					Git: &buildv1.GitBuildSource{
						URI: "http://my.docker/build",
					},
					ContextDir: "context",
				},
				Strategy: buildv1.BuildStrategy{
					DockerStrategy: &buildv1.DockerBuildStrategy{},
				},
				Output: buildv1.BuildOutput{
					To: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: "namespace/builtimage",
					},
				},
			},
		},
	}
}

func RunBuildControllerTest(t testingT, buildClient buildv1clienttyped.BuildsGetter, kClientset kubernetes.Interface, ns string) {
	// Setup an error channel
	errChan := make(chan error) // go routines will send a message on this channel if an error occurs. Once this happens the test is over

	// Create a build
	b, err := buildClient.Builds(ns).Create(mockBuild())
	if err != nil {
		t.Fatal(err)
	}

	// Start watching builds for New -> Pending transition
	buildWatch, err := buildClient.Builds(ns).Watch(metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("metadata.name", b.Name).String(), ResourceVersion: b.ResourceVersion})
	if err != nil {
		t.Fatal(err)
	}
	defer buildWatch.Stop()
	buildModifiedCount := int32(0)
	go func() {
		for e := range buildWatch.ResultChan() {
			if e.Type != watchapi.Modified {
				errChan <- fmt.Errorf("received an unexpected event of type: %s with object: %#v", e.Type, e.Object)
			}
			build, ok := e.Object.(*buildv1.Build)
			if !ok {
				errChan <- fmt.Errorf("received something other than build: %#v", e.Object)
				break
			}
			// If unexpected status, throw error
			if build.Status.Phase != buildv1.BuildPhasePending && build.Status.Phase != buildv1.BuildPhaseNew {
				errChan <- fmt.Errorf("received unexpected build status: %s", build.Status.Phase)
				break
			}
			atomic.AddInt32(&buildModifiedCount, 1)
		}
	}()

	// Watch build pods as they are created
	podWatch, err := kClientset.CoreV1().Pods(ns).Watch(metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("metadata.name",
		buildutil.GetBuildPodName(b)).String()})
	if err != nil {
		t.Fatal(err)
	}
	defer podWatch.Stop()
	podAddedCount := int32(0)
	go func() {
		for e := range podWatch.ResultChan() {
			// Look for creation events
			if e.Type == watchapi.Added {
				atomic.AddInt32(&podAddedCount, 1)
			}
		}
	}()

	select {
	case err := <-errChan:
		t.Errorf("Error: %v", err)
	case <-time.After(BuildControllerTestWait):
		if atomic.LoadInt32(&buildModifiedCount) < 1 {
			t.Errorf("The build was modified an unexpected number of times. Got: %d, Expected: >= 1", buildModifiedCount)
		}
		if atomic.LoadInt32(&podAddedCount) != 1 {
			t.Errorf("The build pod was created an unexpected number of times. Got: %d, Expected: 1", podAddedCount)
		}
	}
}

type buildControllerPodState struct {
	PodPhase   corev1.PodPhase
	BuildPhase buildv1.BuildPhase
}

type buildControllerPodTest struct {
	Name   string
	States []buildControllerPodState
}

func RunBuildControllerPodSyncTest(t testingT, buildClient buildv1clienttyped.BuildsGetter, kClient kubernetes.Interface, ns string) {
	tests := []buildControllerPodTest{
		{
			Name: "running state test",
			States: []buildControllerPodState{
				{
					PodPhase:   corev1.PodRunning,
					BuildPhase: buildv1.BuildPhaseRunning,
				},
			},
		},
		{
			Name: "build succeeded",
			States: []buildControllerPodState{
				{
					PodPhase:   corev1.PodRunning,
					BuildPhase: buildv1.BuildPhaseRunning,
				},
				{
					PodPhase:   corev1.PodSucceeded,
					BuildPhase: buildv1.BuildPhaseComplete,
				},
			},
		},
		{
			Name: "build failed",
			States: []buildControllerPodState{
				{
					PodPhase:   corev1.PodRunning,
					BuildPhase: buildv1.BuildPhaseRunning,
				},
				{
					PodPhase:   corev1.PodFailed,
					BuildPhase: buildv1.BuildPhaseFailed,
				},
			},
		},
	}
	for _, test := range tests {
		// Setup communications channels
		podReadyChan := make(chan *corev1.Pod) // Will receive a value when a build pod is ready
		errChan := make(chan error)            // Will receive a value when an error occurs

		// Create a build
		b, err := buildClient.Builds(ns).Create(mockBuild())
		if err != nil {
			t.Fatal(err)
		}

		// Watch build pod for transition to pending
		podWatch, err := kClient.CoreV1().Pods(ns).Watch(metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("metadata.name",
			buildutil.GetBuildPodName(b)).String()})
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for e := range podWatch.ResultChan() {
				pod, ok := e.Object.(*corev1.Pod)
				if !ok {
					t.Fatalf("%s: unexpected object received: %#v\n", test.Name, e.Object)
				}
				klog.Infof("pod watch event received for pod %s/%s: %v, pod phase: %v", pod.Namespace, pod.Name, e.Type, pod.Status.Phase)
				if pod.Status.Phase == corev1.PodPending {
					podReadyChan <- pod
					break
				}
			}
		}()

		var pod *corev1.Pod
		select {
		case pod = <-podReadyChan:
			if pod.Status.Phase != corev1.PodPending {
				t.Errorf("Got wrong pod phase: %s", pod.Status.Phase)
				podWatch.Stop()
				continue
			}

		case <-time.After(BuildControllersWatchTimeout):
			t.Errorf("Timed out waiting for build pod to be ready")
			podWatch.Stop()
			continue
		}
		podWatch.Stop()

		for _, state := range test.States {
			if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
				// Update pod state and verify that corresponding build state happens accordingly
				pod, err := kClient.CoreV1().Pods(ns).Get(pod.Name, metav1.GetOptions{})
				if err != nil {
					return err
				}
				if pod.Status.Phase == state.PodPhase {
					return fmt.Errorf("another client altered the pod phase to %s: %#v", state.PodPhase, pod)
				}
				pod.Status.Phase = state.PodPhase
				if pod.Status.Phase == corev1.PodSucceeded {
					pod.Status.ContainerStatuses = []corev1.ContainerStatus{
						{
							Name: "container",
							State: corev1.ContainerState{
								Terminated: &corev1.ContainerStateTerminated{
									ExitCode: 0,
								},
							},
						},
					}
				}
				_, err = kClient.CoreV1().Pods(ns).UpdateStatus(pod)
				return err
			}); err != nil {
				t.Fatal(err)
			}

			shouldContinue := func() bool {
				buildWatch, err := buildClient.Builds(ns).Watch(metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("metadata.name", b.Name).String(), ResourceVersion: b.ResourceVersion})
				if err != nil {
					t.Fatal(err)
				}
				defer buildWatch.Stop()

				stateReached := make(chan struct{})
				go func() {
					done := false
					for e := range buildWatch.ResultChan() {
						var ok bool
						b, ok = e.Object.(*buildv1.Build)
						if !ok {
							errChan <- fmt.Errorf("unexpected object received: %#v", e.Object)
							return
						}
						klog.Infof("build watch event received for build %s/%s: %v, build phase: %v", b.Namespace, b.Name, e.Type, b.Status.Phase)
						if e.Type != watchapi.Modified {
							errChan <- fmt.Errorf("unexpected event received: %s, object: %#v", e.Type, e.Object)
							return
						}
						if done && b.Status.Phase != state.BuildPhase {
							errChan <- fmt.Errorf("build %s/%s transitioned to new state (%s) after reaching desired state", b.Namespace, b.Name, b.Status.Phase)
							return
						}
						if b.Status.Phase == state.BuildPhase {
							done = true
							stateReached <- struct{}{}
						}
					}
				}()

				select {
				case err := <-errChan:
					t.Errorf("%s: Error %v", test.Name, err)
					return false
				case <-time.After(BuildControllerTestTransitionTimeout):
					t.Errorf("%s: Timed out waiting for build %s/%s to reach state %s. Current state: %s", test.Name, b.Namespace, b.Name, state.BuildPhase, b.Status.Phase)
					return false
				case <-stateReached:
					klog.Infof("%s: build %s/%s reached desired state of %s", test.Name, b.Namespace, b.Name, state.BuildPhase)
				}

				// After state is reached, continue waiting some time to check for unexpected transitions
				select {
				case err := <-errChan:
					t.Errorf("%s: Error %v", test.Name, err)
					return false

				case <-time.After(BuildControllerTestWait):
					// After waiting for a set time, if no other state is reached, continue to wait for next state transition
					return true
				}
			}()

			if !shouldContinue {
				break
			}
		}
	}
}

func waitForWatch(t testingT, name string, w watchapi.Interface) *watchapi.Event {
	select {
	case e, ok := <-w.ResultChan():
		if !ok {
			t.Fatalf("Channel closed waiting for watch: %s", name)
		}
		return &e
	case <-time.After(BuildControllersWatchTimeout):
		t.Fatalf("Timed out waiting for watch: %s", name)
		return nil
	}
}

func RunImageChangeTriggerTest(t testingT, clusterAdminBuildClient buildv1clienttyped.BuildV1Interface, clusterAdminImageClient imagev1clienttyped.ImageV1Interface, ns string) {
	const (
		tag              = "latest"
		streamName       = "test-image-trigger-repo"
		registryHostname = "registry:8080"
	)

	testutil.SetAdditionalAllowedRegistries(registryHostname)

	imageStream := mockImageStream2(registryHostname, tag)
	imageStreamMapping := mockImageStreamMapping(imageStream.Name, "someimage", tag, registryHostname+"/openshift/test-image-trigger:"+tag)

	config := imageChangeBuildConfig(ns, "sti-imagestreamtag", stiStrategy("ImageStreamTag", streamName+":"+tag))
	_, err := clusterAdminBuildClient.BuildConfigs(ns).Create(config)
	if err != nil {
		t.Fatalf("Couldn't create BuildConfig: %v", err)
	}

	watch, err := clusterAdminBuildClient.Builds(ns).Watch(metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Couldn't subscribe to Builds %v", err)
	}
	defer watch.Stop()

	watch2, err := clusterAdminBuildClient.BuildConfigs(ns).Watch(metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Couldn't subscribe to BuildConfigs %v", err)
	}
	defer watch2.Stop()

	imageStream, err = clusterAdminImageClient.ImageStreams(ns).Create(imageStream)
	if err != nil {
		t.Fatalf("Couldn't create ImageStream: %v", err)
	}

	// give the imagechangecontroller's buildconfig cache time to be updated with the buildconfig object
	// so it doesn't get a miss when looking up the BC while processing the imagestream update event.
	time.Sleep(10 * time.Second)
	_, err = clusterAdminImageClient.ImageStreamMappings(ns).Create(imageStreamMapping)
	if err != nil {
		t.Fatalf("Couldn't create Image: %v", err)
	}

	// wait for initial build event from the creation of the imagerepo with tag latest
	event := waitForWatch(t, "initial build added", watch)
	if e, a := watchapi.Added, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}
	newBuild := event.Object.(*buildv1.Build)
	strategy := newBuild.Spec.Strategy
	if strategy.SourceStrategy.From.Name != registryHostname+"/openshift/test-image-trigger:"+tag {
		i, _ := clusterAdminImageClient.ImageStreams(ns).Get(imageStream.Name, metav1.GetOptions{})
		bc, _ := clusterAdminBuildClient.BuildConfigs(ns).Get(config.Name, metav1.GetOptions{})
		t.Fatalf("Expected build with base image %s, got %s\n, imagerepo is %v\ntrigger is %s\n", registryHostname+"/openshift/test-image-trigger:"+tag, strategy.SourceStrategy.From.Name, i, bc.Spec.Triggers[0].ImageChange)
	}
	// Wait for an update on the specific build that was added
	watch3, err := clusterAdminBuildClient.Builds(ns).Watch(metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("metadata.name", newBuild.Name).String(), ResourceVersion: newBuild.ResourceVersion})
	defer watch3.Stop()
	if err != nil {
		t.Fatalf("Couldn't subscribe to Builds %v", err)
	}
	event = waitForWatch(t, "initial build update", watch3)
	if e, a := watchapi.Modified, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}
	newBuild = event.Object.(*buildv1.Build)
	// Make sure the resolution of the build's docker image pushspec didn't mutate the persisted API object
	if newBuild.Spec.Output.To.Name != "test-image-trigger-repo:outputtag" {
		t.Fatalf("unexpected build output: %#v %#v", newBuild.Spec.Output.To, newBuild.Spec.Output)
	}
	if newBuild.Labels["testlabel"] != "testvalue" {
		t.Fatalf("Expected build with label %s=%s from build config got %s=%s", "testlabel", "testvalue", "testlabel", newBuild.Labels["testlabel"])
	}

	// wait for build config to be updated
	timeout := time.After(BuildControllerTestWait)
WaitLoop:
	for {
		select {
		case e, ok := <-watch2.ResultChan():
			if !ok {
				t.Fatalf("Channel closed waiting for watch: build config update in WaitLoop")
			}
			event = &e
			continue
		case <-timeout:
			break WaitLoop
		}
	}
	updatedConfig := event.Object.(*buildv1.BuildConfig)
	if err != nil {
		t.Fatalf("Couldn't get BuildConfig: %v", err)
	}
	// the first tag did not have an image id, so the last trigger field is the pull spec
	if updatedConfig.Spec.Triggers[0].ImageChange.LastTriggeredImageID != registryHostname+"/openshift/test-image-trigger:"+tag {
		t.Fatalf("Expected imageID equal to pull spec, got %#v", updatedConfig.Spec.Triggers[0].ImageChange)
	}

	// clear out the build/buildconfig watches before triggering a new build
	timeout = time.After(60 * time.Second)
WaitLoop2:
	for {
		select {
		case _, ok := <-watch.ResultChan():
			if !ok {
				t.Fatalf("Channel closed waiting for watch: build update in WaitLoop2")
			}
			continue
		case _, ok := <-watch2.ResultChan():
			if !ok {
				t.Fatalf("Channel closed waiting for watch: build config update in WaitLoop2")
			}
			continue
		case <-timeout:
			break WaitLoop2
		}
	}

	// trigger a build by posting a new image
	if _, err := clusterAdminImageClient.ImageStreamMappings(ns).Create(&imagev1.ImageStreamMapping{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      imageStream.Name,
		},
		Tag: tag,
		Image: imagev1.Image{
			ObjectMeta: metav1.ObjectMeta{
				Name: "ref-2-random",
			},
			DockerImageReference: registryHostname + "/openshift/test-image-trigger:ref-2-random",
		},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	event = waitForWatch(t, "second build created", watch)
	if e, a := watchapi.Added, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}
	newBuild = event.Object.(*buildv1.Build)
	strategy = newBuild.Spec.Strategy
	if strategy.SourceStrategy.From.Name != registryHostname+"/openshift/test-image-trigger:ref-2-random" {
		i, _ := clusterAdminImageClient.ImageStreams(ns).Get(imageStream.Name, metav1.GetOptions{})
		bc, _ := clusterAdminBuildClient.BuildConfigs(ns).Get(config.Name, metav1.GetOptions{})
		t.Fatalf("Expected build with base image %s, got %s\n, imagerepo is %v\trigger is %s\n", registryHostname+"/openshift/test-image-trigger:ref-2-random", strategy.SourceStrategy.From.Name, i, bc.Spec.Triggers[3].ImageChange)
	}

	// Listen to events on specific  build
	watch4, err := clusterAdminBuildClient.Builds(ns).Watch(metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("metadata.name", newBuild.Name).String(), ResourceVersion: newBuild.ResourceVersion})
	defer watch4.Stop()

	event = waitForWatch(t, "update on second build", watch4)
	if e, a := watchapi.Modified, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}
	newBuild = event.Object.(*buildv1.Build)
	// Make sure the resolution of the build's docker image pushspec didn't mutate the persisted API object
	if newBuild.Spec.Output.To.Name != "test-image-trigger-repo:outputtag" {
		t.Fatalf("unexpected build output: %#v %#v", newBuild.Spec.Output.To, newBuild.Spec.Output)
	}
	if newBuild.Labels["testlabel"] != "testvalue" {
		t.Fatalf("Expected build with label %s=%s from build config got %s=%s", "testlabel", "testvalue", "testlabel", newBuild.Labels["testlabel"])
	}

	timeout = time.After(BuildControllerTestWait)
WaitLoop3:
	for {
		select {
		case e, ok := <-watch2.ResultChan():
			if !ok {
				t.Fatalf("Channel closed waiting for watch: build config update in WaitLoop3")
			}
			event = &e
			continue
		case <-timeout:
			break WaitLoop3
		}
	}
	updatedConfig = event.Object.(*buildv1.BuildConfig)
	if e, a := registryHostname+"/openshift/test-image-trigger:ref-2-random", updatedConfig.Spec.Triggers[0].ImageChange.LastTriggeredImageID; e != a {
		t.Errorf("unexpected trigger id: expected %v, got %v", e, a)
	}
}

func RunBuildDeleteTest(t testingT, clusterAdminClient buildv1clienttyped.BuildsGetter, clusterAdminKubeClientset kubernetes.Interface, ns string) {
	buildWatch, err := clusterAdminClient.Builds(ns).Watch(metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Couldn't subscribe to Builds %v", err)
	}
	defer buildWatch.Stop()

	_, err = clusterAdminClient.Builds(ns).Create(mockBuild())
	if err != nil {
		t.Fatalf("Couldn't create Build: %v", err)
	}

	podWatch, err := clusterAdminKubeClientset.CoreV1().Pods(ns).Watch(metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Couldn't subscribe to Pods %v", err)
	}
	defer podWatch.Stop()

	// wait for initial build event from the creation of the imagerepo with tag latest
	event := waitForWatch(t, "initial build added", buildWatch)
	if e, a := watchapi.Added, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}
	newBuild := event.Object.(*buildv1.Build)

	// initial pod creation for build
	event = waitForWatch(t, "build pod created", podWatch)
	if e, a := watchapi.Added, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}

	clusterAdminClient.Builds(ns).Delete(newBuild.Name, nil)

	event = waitForWatchType(t, "pod deleted due to build deleted", podWatch, watchapi.Deleted)
	if e, a := watchapi.Deleted, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}
	pod := event.Object.(*corev1.Pod)
	if expected := buildutil.GetBuildPodName(newBuild); pod.Name != expected {
		t.Fatalf("Expected pod %s to be deleted, but pod %s was deleted", expected, pod.Name)
	}

}

// waitForWatchType tolerates receiving 3 events before failing while watching for a particular event
// type.
func waitForWatchType(t testingT, name string, w watchapi.Interface, expect watchapi.EventType) *watchapi.Event {
	tries := 3
	for i := 0; i < tries; i++ {
		select {
		case e := <-w.ResultChan():
			if e.Type != expect {
				continue
			}
			return &e
		case <-time.After(BuildControllersWatchTimeout):
			t.Fatalf("Timed out waiting for watch: %s", name)
			return nil
		}
	}
	t.Fatalf("Waited for a %v event with %d tries but never received one", expect, tries)
	return nil
}

func RunBuildRunningPodDeleteTest(t testingT, clusterAdminClient buildv1clienttyped.BuildsGetter, clusterAdminKubeClientset kubernetes.Interface, ns string) {

	buildWatch, err := clusterAdminClient.Builds(ns).Watch(metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Couldn't subscribe to Builds %v", err)
	}
	defer buildWatch.Stop()

	_, err = clusterAdminClient.Builds(ns).Create(mockBuild())
	if err != nil {
		t.Fatalf("Couldn't create Build: %v", err)
	}

	podWatch, err := clusterAdminKubeClientset.CoreV1().Pods(ns).Watch(metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Couldn't subscribe to Pods %v", err)
	}
	defer podWatch.Stop()

	// wait for initial build event from the creation of the imagerepo with tag latest
	event := waitForWatch(t, "initial build added", buildWatch)
	if e, a := watchapi.Added, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}
	newBuild := event.Object.(*buildv1.Build)
	buildName := newBuild.Name
	podName := newBuild.Name + "-build"

	// initial pod creation for build
	for {
		event = waitForWatch(t, "build pod created", podWatch)
		newPod := event.Object.(*corev1.Pod)
		if newPod.Name == podName {
			break
		}
	}
	if e, a := watchapi.Added, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}

	// throw away events from other builds, we only care about the new build
	// we just triggered
	for {
		event = waitForWatch(t, "build updated to pending", buildWatch)
		newBuild = event.Object.(*buildv1.Build)
		if newBuild.Name == buildName {
			break
		}
	}
	if e, a := watchapi.Modified, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}
	if newBuild.Status.Phase != buildv1.BuildPhasePending {
		t.Fatalf("expected build status to be marked pending, but was marked %s", newBuild.Status.Phase)
	}

	clusterAdminKubeClientset.CoreV1().Pods(ns).Delete(buildutil.GetBuildPodName(newBuild), metav1.NewDeleteOptions(0))
	event = waitForWatch(t, "build updated to error", buildWatch)
	if e, a := watchapi.Modified, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}
	newBuild = event.Object.(*buildv1.Build)
	if newBuild.Status.Phase != buildv1.BuildPhaseError {
		t.Fatalf("expected build status to be marked error, but was marked %s", newBuild.Status.Phase)
	}

	foundFailed := false

	err = wait.Poll(time.Second, 30*time.Second, func() (bool, error) {
		events, err := clusterAdminKubeClientset.CoreV1().Events(ns).Search(legacyscheme.Scheme, newBuild)
		if err != nil {
			t.Fatalf("error getting build events: %v", err)
			return false, fmt.Errorf("error getting build events: %v", err)
		}
		for _, event := range events.Items {
			if event.Reason == buildutil.BuildFailedEventReason {
				foundFailed = true
				expect := fmt.Sprintf(buildutil.BuildFailedEventMessage, newBuild.Namespace, newBuild.Name)
				if event.Message != expect {
					return false, fmt.Errorf("expected failed event message to be %s, got %s", expect, event.Message)
				}
				return true, nil
			}
		}
		return false, nil
	})

	if err != nil {
		t.Fatalf("unexpected: %v", err)
		return
	}

	if !foundFailed {
		t.Fatalf("expected to find a failed event on the build %s/%s", newBuild.Namespace, newBuild.Name)
	}
}

func RunBuildCompletePodDeleteTest(t testingT, clusterAdminClient buildv1clienttyped.BuildsGetter, clusterAdminKubeClientset kubernetes.Interface, ns string) {

	buildWatch, err := clusterAdminClient.Builds(ns).Watch(metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Couldn't subscribe to Builds %v", err)
	}
	defer buildWatch.Stop()

	_, err = clusterAdminClient.Builds(ns).Create(mockBuild())
	if err != nil {
		t.Fatalf("Couldn't create Build: %v", err)
	}

	podWatch, err := clusterAdminKubeClientset.CoreV1().Pods(ns).Watch(metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Couldn't subscribe to Pods %v", err)
	}
	defer podWatch.Stop()

	// wait for initial build event from the creation of the imagerepo with tag latest
	event := waitForWatch(t, "initial build added", buildWatch)
	if e, a := watchapi.Added, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}
	newBuild := event.Object.(*buildv1.Build)

	// initial pod creation for build
	event = waitForWatch(t, "build pod created", podWatch)
	if e, a := watchapi.Added, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}

	event = waitForWatch(t, "build updated to pending", buildWatch)
	if e, a := watchapi.Modified, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}

	newBuild = event.Object.(*buildv1.Build)
	if newBuild.Status.Phase != buildv1.BuildPhasePending {
		t.Fatalf("expected build status to be marked pending, but was marked %s", newBuild.Status.Phase)
	}

	newBuild.Status.Phase = buildv1.BuildPhaseComplete
	clusterAdminClient.Builds(ns).Update(newBuild)
	event = waitForWatch(t, "build updated to complete", buildWatch)
	if e, a := watchapi.Modified, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}
	newBuild = event.Object.(*buildv1.Build)
	if newBuild.Status.Phase != buildv1.BuildPhaseComplete {
		t.Fatalf("expected build status to be marked complete, but was marked %s", newBuild.Status.Phase)
	}

	clusterAdminKubeClientset.CoreV1().Pods(ns).Delete(buildutil.GetBuildPodName(newBuild), metav1.NewDeleteOptions(0))
	time.Sleep(10 * time.Second)
	newBuild, err = clusterAdminClient.Builds(ns).Get(newBuild.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if newBuild.Status.Phase != buildv1.BuildPhaseComplete {
		t.Fatalf("build status was updated to %s after deleting pod, should have stayed as %s", newBuild.Status.Phase, buildv1.BuildPhaseComplete)
	}
}

func RunBuildConfigChangeControllerTest(t testingT, clusterAdminBuildClient buildv1clienttyped.BuildV1Interface, ns string) {
	config := configChangeBuildConfig(ns)
	created, err := clusterAdminBuildClient.BuildConfigs(ns).Create(config)
	if err != nil {
		t.Fatalf("Couldn't create BuildConfig: %v", err)
	}

	watch, err := clusterAdminBuildClient.Builds(ns).Watch(metav1.ListOptions{})
	if err != nil {
		t.Fatalf("Couldn't subscribe to Builds %v", err)
	}
	defer watch.Stop()

	watch2, err := clusterAdminBuildClient.BuildConfigs(ns).Watch(metav1.ListOptions{ResourceVersion: created.ResourceVersion})
	if err != nil {
		t.Fatalf("Couldn't subscribe to BuildConfigs %v", err)
	}
	defer watch2.Stop()

	// wait for initial build event
	event := waitForWatch(t, "config change initial build added", watch)
	if e, a := watchapi.Added, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}

	event = waitForWatch(t, "config change config updated", watch2)
	if e, a := watchapi.Modified, event.Type; e != a {
		t.Fatalf("expected watch event type %s, got %s", e, a)
	}
	if bc := event.Object.(*buildv1.BuildConfig); bc.Status.LastVersion == 0 {
		t.Fatalf("expected build config lastversion to be greater than zero after build")
	}
}

func configChangeBuildConfig(ns string) *buildv1.BuildConfig {
	bc := &buildv1.BuildConfig{}
	bc.Name = "testcfgbc"
	bc.Namespace = ns
	bc.Spec.Source.Git = &buildv1.GitBuildSource{}
	bc.Spec.Source.Git.URI = "git://github.com/openshift/ruby-hello-world.git"
	bc.Spec.Strategy.DockerStrategy = &buildv1.DockerBuildStrategy{}
	configChangeTrigger := buildv1.BuildTriggerPolicy{Type: buildv1.ConfigChangeBuildTriggerType}
	bc.Spec.Triggers = append(bc.Spec.Triggers, configChangeTrigger)
	return bc
}

func mockImageStream2(registryHostname, tag string) *imagev1.ImageStream {
	return &imagev1.ImageStream{
		ObjectMeta: metav1.ObjectMeta{Name: "test-image-trigger-repo"},

		Spec: imagev1.ImageStreamSpec{
			DockerImageRepository: registryHostname + "/openshift/test-image-trigger",
			Tags: []imagev1.TagReference{
				{
					Name: tag,
					From: &corev1.ObjectReference{
						Kind: "DockerImage",
						Name: registryHostname + "/openshift/test-image-trigger:" + tag,
					},
				},
			},
		},
	}
}

func mockImageStreamMapping(stream, image, tag, reference string) *imagev1.ImageStreamMapping {
	// create a mapping to an image that doesn't exist
	return &imagev1.ImageStreamMapping{
		ObjectMeta: metav1.ObjectMeta{Name: stream},
		Tag:        tag,
		Image: imagev1.Image{
			ObjectMeta: metav1.ObjectMeta{
				Name: image,
			},
			DockerImageReference: reference,
		},
	}
}

func imageChangeBuildConfig(ns, name string, strategy buildv1.BuildStrategy) *buildv1.BuildConfig {
	return &buildv1.BuildConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    map[string]string{"testlabel": "testvalue"},
		},
		Spec: buildv1.BuildConfigSpec{

			RunPolicy: buildv1.BuildRunPolicyParallel,
			CommonSpec: buildv1.CommonSpec{
				Source: buildv1.BuildSource{
					Git: &buildv1.GitBuildSource{
						URI: "git://github.com/openshift/ruby-hello-world.git",
					},
					ContextDir: "contextimage",
				},
				Strategy: strategy,
				Output: buildv1.BuildOutput{
					To: &corev1.ObjectReference{
						Kind: "ImageStreamTag",
						Name: "test-image-trigger-repo:outputtag",
					},
				},
			},
			Triggers: []buildv1.BuildTriggerPolicy{
				{
					Type:        buildv1.ImageChangeBuildTriggerType,
					ImageChange: &buildv1.ImageChangeTrigger{},
				},
			},
		},
	}
}

func stiStrategy(kind, name string) buildv1.BuildStrategy {
	return buildv1.BuildStrategy{
		SourceStrategy: &buildv1.SourceBuildStrategy{
			From: corev1.ObjectReference{
				Kind: kind,
				Name: name,
			},
		},
	}
}
