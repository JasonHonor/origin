package api_test

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/google/gofuzz"

	"k8s.io/apimachinery/pkg/api/apitesting"
	"k8s.io/apimachinery/pkg/api/apitesting/fuzzer"
	"k8s.io/apimachinery/pkg/api/apitesting/roundtrip"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/kubernetes/pkg/api/legacyscheme"
	kapitesting "k8s.io/kubernetes/pkg/api/testing"
	"k8s.io/kubernetes/pkg/apis/apps"
	kapi "k8s.io/kubernetes/pkg/apis/core"
	"k8s.io/kubernetes/pkg/apis/core/v1"
	"k8s.io/kubernetes/pkg/apis/core/validation"
	extensionsv1beta1 "k8s.io/kubernetes/pkg/apis/extensions/v1beta1"

	buildv1 "github.com/openshift/api/build/v1"
	"github.com/openshift/library-go/pkg/image/imageutil"
	oapps "github.com/openshift/origin/pkg/apps/apis/apps"
	authorizationapi "github.com/openshift/origin/pkg/authorization/apis/authorization"
	build "github.com/openshift/origin/pkg/build/apis/build"
	image "github.com/openshift/origin/pkg/image/apis/image"
	oauthapi "github.com/openshift/origin/pkg/oauth/apis/oauth"
	routeapi "github.com/openshift/origin/pkg/route/apis/route"
	securityapi "github.com/openshift/origin/pkg/security/apis/security"
	templateapi "github.com/openshift/origin/pkg/template/apis/template"
	uservalidation "github.com/openshift/origin/pkg/user/apis/user/validation"

	// install all APIs
	_ "github.com/openshift/origin/pkg/api/install"
	_ "github.com/openshift/origin/pkg/quota/apis/quota/install"
	_ "k8s.io/kubernetes/pkg/apis/core/install"
)

func originFuzzer(t *testing.T, seed int64) *fuzz.Fuzzer {
	f := fuzzer.FuzzerFor(kapitesting.FuzzerFuncs, rand.NewSource(seed), legacyscheme.Codecs)
	f.Funcs(
		// Roles and RoleBindings maps are never nil
		func(j *authorizationapi.RoleBinding, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			for i := range j.Subjects {
				kinds := []string{authorizationapi.UserKind, authorizationapi.SystemUserKind, authorizationapi.GroupKind, authorizationapi.SystemGroupKind, authorizationapi.ServiceAccountKind}
				j.Subjects[i].Kind = kinds[c.Intn(len(kinds))]
				switch j.Subjects[i].Kind {
				case authorizationapi.UserKind:
					j.Subjects[i].Namespace = ""
					if len(uservalidation.ValidateUserName(j.Subjects[i].Name, false)) != 0 {
						j.Subjects[i].Name = fmt.Sprintf("validusername%d", i)
					}

				case authorizationapi.GroupKind:
					j.Subjects[i].Namespace = ""
					if len(uservalidation.ValidateGroupName(j.Subjects[i].Name, false)) != 0 {
						j.Subjects[i].Name = fmt.Sprintf("validgroupname%d", i)
					}

				case authorizationapi.ServiceAccountKind:
					if len(validation.ValidateNamespaceName(j.Subjects[i].Namespace, false)) != 0 {
						j.Subjects[i].Namespace = fmt.Sprintf("sanamespacehere%d", i)
					}
					if len(validation.ValidateServiceAccountName(j.Subjects[i].Name, false)) != 0 {
						j.Subjects[i].Name = fmt.Sprintf("sanamehere%d", i)
					}

				case authorizationapi.SystemUserKind, authorizationapi.SystemGroupKind:
					j.Subjects[i].Namespace = ""
					j.Subjects[i].Name = ":" + j.Subjects[i].Name

				}

				j.Subjects[i].UID = types.UID("")
				j.Subjects[i].APIVersion = ""
				j.Subjects[i].ResourceVersion = ""
				j.Subjects[i].FieldPath = ""
			}
		},
		func(j *authorizationapi.PolicyRule, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			// if no groups are found, then we assume "".  This matches defaulting
			if len(j.APIGroups) == 0 {
				j.APIGroups = []string{""}
			}
			j.AttributeRestrictions = nil
		},
		func(j *authorizationapi.ClusterRoleBinding, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			for i := range j.Subjects {
				kinds := []string{authorizationapi.UserKind, authorizationapi.SystemUserKind, authorizationapi.GroupKind, authorizationapi.SystemGroupKind, authorizationapi.ServiceAccountKind}
				j.Subjects[i].Kind = kinds[c.Intn(len(kinds))]
				switch j.Subjects[i].Kind {
				case authorizationapi.UserKind:
					j.Subjects[i].Namespace = ""
					if len(uservalidation.ValidateUserName(j.Subjects[i].Name, false)) != 0 {
						j.Subjects[i].Name = fmt.Sprintf("validusername%d", i)
					}

				case authorizationapi.GroupKind:
					j.Subjects[i].Namespace = ""
					if len(uservalidation.ValidateGroupName(j.Subjects[i].Name, false)) != 0 {
						j.Subjects[i].Name = fmt.Sprintf("validgroupname%d", i)
					}

				case authorizationapi.ServiceAccountKind:
					if len(validation.ValidateNamespaceName(j.Subjects[i].Namespace, false)) != 0 {
						j.Subjects[i].Namespace = fmt.Sprintf("sanamespacehere%d", i)
					}
					if len(validation.ValidateServiceAccountName(j.Subjects[i].Name, false)) != 0 {
						j.Subjects[i].Name = fmt.Sprintf("sanamehere%d", i)
					}

				case authorizationapi.SystemUserKind, authorizationapi.SystemGroupKind:
					j.Subjects[i].Namespace = ""
					j.Subjects[i].Name = ":" + j.Subjects[i].Name

				}

				j.Subjects[i].UID = types.UID("")
				j.Subjects[i].APIVersion = ""
				j.Subjects[i].ResourceVersion = ""
				j.Subjects[i].FieldPath = ""
			}
		},
		func(j *templateapi.Template, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			j.Objects = nil

			objs := []runtime.Object{
				&kapi.Pod{},
				&apps.Deployment{},
				&kapi.Service{},
				&build.BuildConfig{},
			}

			for _, obj := range objs {
				c.Fuzz(obj)

				var codec runtime.Codec
				switch obj.(type) {
				case *apps.Deployment:
					codec = apitesting.TestCodec(legacyscheme.Codecs, extensionsv1beta1.SchemeGroupVersion)
				case *build.BuildConfig:
					codec = apitesting.TestCodec(legacyscheme.Codecs, buildv1.SchemeGroupVersion)
				default:
					codec = apitesting.TestCodec(legacyscheme.Codecs, v1.SchemeGroupVersion)
				}

				b, err := runtime.Encode(codec, obj)
				if err != nil {
					t.Error(err)
					return
				}

				j.Objects = append(j.Objects,
					&runtime.Unknown{
						ContentType: "application/json",
						Raw:         bytes.TrimRight(b, "\n"),
					},
				)
			}

			j.Objects = append(j.Objects, &runtime.Unknown{
				ContentType: "application/json",
				Raw:         []byte(`{"kind":"Foo","apiVersion":"mygroup/v1","complex":{"a":true},"list":["item"],"bool":true,"int":1,"string":"hello"}`),
			})
		},
		func(j *image.Image, c fuzz.Continue) {
			c.Fuzz(&j.ObjectMeta)
			c.Fuzz(&j.DockerImageMetadata)
			c.Fuzz(&j.Signatures)
			j.DockerImageMetadata.APIVersion = ""
			j.DockerImageMetadata.Kind = ""
			j.DockerImageMetadataVersion = []string{"pre012", "1.0"}[c.Rand.Intn(2)]
			j.DockerImageReference = c.RandString()
		},
		func(j *image.ImageSignature, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			j.Conditions = nil
			j.ImageIdentity = ""
			j.SignedClaims = nil
			j.Created = nil
			j.IssuedBy = nil
			j.IssuedTo = nil
		},
		func(j *image.ImageStream, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			for k, v := range j.Spec.Tags {
				if len(v.ReferencePolicy.Type) == 0 {
					v.ReferencePolicy.Type = image.SourceTagReferencePolicy
					j.Spec.Tags[k] = v
				}
			}
		},
		func(j *image.ImageStreamMapping, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			j.DockerImageRepository = ""
		},
		func(j *image.ImageImportSpec, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			if j.To == nil {
				// To is defaulted to be not nil
				j.To = &kapi.LocalObjectReference{}
			}
		},
		func(j *image.ImageStreamImage, c fuzz.Continue) {
			c.Fuzz(&j.Image)
			// because we de-embedded Image from ImageStreamImage, in order to round trip
			// successfully, the ImageStreamImage's ObjectMeta must match the Image's.
			j.ObjectMeta = j.Image.ObjectMeta
		},
		func(j *image.ImageStreamSpec, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			// if the generated fuzz value has a tag or image id, strip it
			if strings.ContainsAny(j.DockerImageRepository, ":@") {
				j.DockerImageRepository = ""
			}
			if j.Tags == nil {
				j.Tags = make(map[string]image.TagReference)
			}
			for k, v := range j.Tags {
				v.Name = k
				j.Tags[k] = v
			}
		},
		func(j *image.ImageStreamStatus, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			// if the generated fuzz value has a tag or image id, strip it
			if strings.ContainsAny(j.DockerImageRepository, ":@") {
				j.DockerImageRepository = ""
			}
		},
		func(j *image.ImageStreamTag, c fuzz.Continue) {
			c.Fuzz(&j.Image)
			// because we de-embedded Image from ImageStreamTag, in order to round trip
			// successfully, the ImageStreamTag's ObjectMeta must match the Image's.
			j.ObjectMeta = j.Image.ObjectMeta
		},
		func(j *image.TagReference, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			if j.From != nil {
				specs := []string{"", "ImageStreamTag", "ImageStreamImage"}
				j.From.Kind = specs[c.Intn(len(specs))]
			}
		},
		func(j *image.TagReferencePolicy, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			j.Type = image.SourceTagReferencePolicy
		},
		func(j *build.BuildConfigSpec, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			j.RunPolicy = build.BuildRunPolicySerial
		},
		func(j *build.SourceBuildStrategy, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			j.From.Kind = "ImageStreamTag"
			j.From.Name = "image:tag"
			j.From.APIVersion = ""
			j.From.ResourceVersion = ""
			j.From.FieldPath = ""
		},
		func(j *build.CustomBuildStrategy, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			j.From.Kind = "ImageStreamTag"
			j.From.Name = "image:tag"
			j.From.APIVersion = ""
			j.From.ResourceVersion = ""
			j.From.FieldPath = ""
		},
		func(j *build.DockerBuildStrategy, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			if j.From != nil {
				j.From.Kind = "ImageStreamTag"
				j.From.Name = "image:tag"
				j.From.APIVersion = ""
				j.From.ResourceVersion = ""
				j.From.FieldPath = ""
			}
		},
		func(j *build.BuildOutput, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			if j.To != nil && (len(j.To.Kind) == 0 || j.To.Kind == "ImageStream") {
				j.To.Kind = "ImageStreamTag"
			}
			if j.To != nil && strings.Contains(j.To.Name, ":") {
				j.To.Name = strings.Replace(j.To.Name, ":", "-", -1)
			}
		},
		func(j *routeapi.RouteTargetReference, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			j.Kind = "Service"
			j.Weight = new(int32)
			*j.Weight = 100
		},
		func(j *routeapi.TLSConfig, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			if len(j.Termination) == 0 && len(j.DestinationCACertificate) == 0 {
				j.Termination = routeapi.TLSTerminationEdge
			}
		},
		func(j *oapps.DeploymentConfig, c fuzz.Continue) {
			c.FuzzNoCustom(j)

			if len(j.Spec.Selector) == 0 && j.Spec.Template != nil {
				j.Spec.Selector = j.Spec.Template.Labels
			}

			j.Spec.Triggers = []oapps.DeploymentTriggerPolicy{{Type: oapps.DeploymentTriggerOnConfigChange}}
			if j.Spec.Template != nil && len(j.Spec.Template.Spec.Containers) == 1 {
				containerName := j.Spec.Template.Spec.Containers[0].Name
				if p := j.Spec.Strategy.RecreateParams; p != nil {
					defaultHookContainerName(p.Pre, containerName)
					defaultHookContainerName(p.Mid, containerName)
					defaultHookContainerName(p.Post, containerName)
				}
				if p := j.Spec.Strategy.RollingParams; p != nil {
					defaultHookContainerName(p.Pre, containerName)
					defaultHookContainerName(p.Post, containerName)
				}
			}
		},
		func(j *oapps.DeploymentStrategy, c fuzz.Continue) {
			randInt64 := func() *int64 {
				p := int64(c.RandUint64())
				return &p
			}
			c.FuzzNoCustom(j)
			j.RecreateParams, j.RollingParams, j.CustomParams = nil, nil, nil
			strategyTypes := []oapps.DeploymentStrategyType{oapps.DeploymentStrategyTypeRecreate, oapps.DeploymentStrategyTypeRolling, oapps.DeploymentStrategyTypeCustom}
			j.Type = strategyTypes[c.Rand.Intn(len(strategyTypes))]
			j.ActiveDeadlineSeconds = randInt64()
			switch j.Type {
			case oapps.DeploymentStrategyTypeRecreate:
				params := &oapps.RecreateDeploymentStrategyParams{}
				c.Fuzz(params)
				if params.TimeoutSeconds == nil {
					s := int64(120)
					params.TimeoutSeconds = &s
				}
				j.RecreateParams = params
			case oapps.DeploymentStrategyTypeRolling:
				params := &oapps.RollingDeploymentStrategyParams{}
				params.TimeoutSeconds = randInt64()
				params.IntervalSeconds = randInt64()
				params.UpdatePeriodSeconds = randInt64()

				policyTypes := []oapps.LifecycleHookFailurePolicy{
					oapps.LifecycleHookFailurePolicyRetry,
					oapps.LifecycleHookFailurePolicyAbort,
					oapps.LifecycleHookFailurePolicyIgnore,
				}
				if c.RandBool() {
					params.Pre = &oapps.LifecycleHook{
						FailurePolicy: policyTypes[c.Rand.Intn(len(policyTypes))],
						ExecNewPod: &oapps.ExecNewPodHook{
							ContainerName: c.RandString(),
						},
					}
				}
				if c.RandBool() {
					params.Post = &oapps.LifecycleHook{
						FailurePolicy: policyTypes[c.Rand.Intn(len(policyTypes))],
						ExecNewPod: &oapps.ExecNewPodHook{
							ContainerName: c.RandString(),
						},
					}
				}
				if c.RandBool() {
					params.MaxUnavailable = intstr.FromInt(int(c.Rand.Int31()))
					params.MaxSurge = intstr.FromInt(int(c.Rand.Int31()))
				} else {
					params.MaxSurge = intstr.FromString(fmt.Sprintf("%d%%", c.RandUint64()))
					params.MaxUnavailable = intstr.FromString(fmt.Sprintf("%d%%", c.RandUint64()))
				}
				j.RollingParams = params
			}
		},
		func(j *oapps.DeploymentCauseImageTrigger, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			specs := []string{"", "a/b", "a/b/c", "a:5000/b/c", "a/b", "a/b"}
			tags := []string{"stuff", "other"}
			j.From.Name = specs[c.Intn(len(specs))]
			if len(j.From.Name) > 0 {
				j.From.Name = imageutil.JoinImageStreamTag(j.From.Name, tags[c.Intn(len(tags))])
			}
		},
		func(j *oapps.DeploymentTriggerImageChangeParams, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			specs := []string{"a/b", "a/b/c", "a:5000/b/c", "a/b:latest", "a/b@test"}
			j.From.Kind = "DockerImage"
			j.From.Name = specs[c.Intn(len(specs))]
		},

		// TODO: uncomment when round tripping for init containers is available (the annotation is
		// not supported on security context review for now)
		func(j *securityapi.PodSecurityPolicyReview, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			j.Spec.Template.Spec.InitContainers = nil
			for i := range j.Status.AllowedServiceAccounts {
				j.Status.AllowedServiceAccounts[i].Template.Spec.InitContainers = nil
			}
		},
		func(j *securityapi.PodSecurityPolicySelfSubjectReview, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			j.Spec.Template.Spec.InitContainers = nil
			j.Status.Template.Spec.InitContainers = nil
		},
		func(j *securityapi.PodSecurityPolicySubjectReview, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			j.Spec.Template.Spec.InitContainers = nil
			j.Status.Template.Spec.InitContainers = nil
		},
		func(j *oauthapi.OAuthAuthorizeToken, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			if len(j.CodeChallenge) > 0 && len(j.CodeChallengeMethod) == 0 {
				j.CodeChallengeMethod = "plain"
			}
		},
		func(j *oauthapi.OAuthClientAuthorization, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			if len(j.Scopes) == 0 {
				j.Scopes = append(j.Scopes, "user:full")
			}
		},
		func(j *routeapi.RouteSpec, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			if len(j.WildcardPolicy) == 0 {
				j.WildcardPolicy = routeapi.WildcardPolicyNone
			}
		},
		func(j *routeapi.RouteIngress, c fuzz.Continue) {
			c.FuzzNoCustom(j)
			if len(j.WildcardPolicy) == 0 {
				j.WildcardPolicy = routeapi.WildcardPolicyNone
			}
		},

		func(j *runtime.Object, c fuzz.Continue) {
			// runtime.EmbeddedObject causes a panic inside of fuzz because runtime.Object isn't handled.
		},
		func(t *time.Time, c fuzz.Continue) {
			// This is necessary because the standard fuzzed time.Time object is
			// completely nil, but when JSON unmarshals dates it fills in the
			// unexported loc field with the time.UTC object, resulting in
			// reflect.DeepEqual returning false in the round trip tests. We solve it
			// by using a date that will be identical to the one JSON unmarshals.
			*t = time.Date(2000, 1, 1, 1, 1, 1, 0, time.UTC)
		},
		func(u64 *uint64, c fuzz.Continue) {
			// TODO: uint64's are NOT handled right.
			*u64 = c.RandUint64() >> 8
		},

		func(scc *securityapi.SecurityContextConstraints, c fuzz.Continue) {
			c.FuzzNoCustom(scc) // fuzz self without calling this function again
			userTypes := []securityapi.RunAsUserStrategyType{securityapi.RunAsUserStrategyMustRunAsNonRoot, securityapi.RunAsUserStrategyMustRunAs, securityapi.RunAsUserStrategyRunAsAny, securityapi.RunAsUserStrategyMustRunAsRange}
			scc.RunAsUser.Type = userTypes[c.Rand.Intn(len(userTypes))]
			seLinuxTypes := []securityapi.SELinuxContextStrategyType{securityapi.SELinuxStrategyRunAsAny, securityapi.SELinuxStrategyMustRunAs}
			scc.SELinuxContext.Type = seLinuxTypes[c.Rand.Intn(len(seLinuxTypes))]
			supGroupTypes := []securityapi.SupplementalGroupsStrategyType{securityapi.SupplementalGroupsStrategyMustRunAs, securityapi.SupplementalGroupsStrategyRunAsAny}
			scc.SupplementalGroups.Type = supGroupTypes[c.Rand.Intn(len(supGroupTypes))]
			fsGroupTypes := []securityapi.FSGroupStrategyType{securityapi.FSGroupStrategyMustRunAs, securityapi.FSGroupStrategyRunAsAny}
			scc.FSGroup.Type = fsGroupTypes[c.Rand.Intn(len(fsGroupTypes))]
			// avoid the defaulting logic for this field by making it never nil
			allowPrivilegeEscalation := c.RandBool()
			scc.AllowPrivilegeEscalation = &allowPrivilegeEscalation

			// when fuzzing the volume types ensure it is set to avoid the defaulter's expansion.
			// Do not use FSTypeAll or host dir setting to steer clear of defaulting mechanics
			// which are covered in specific unit tests.
			volumeTypes := []securityapi.FSType{securityapi.FSTypeAWSElasticBlockStore,
				securityapi.FSTypeAzureFile,
				securityapi.FSTypeCephFS,
				securityapi.FSTypeCinder,
				securityapi.FSTypeDownwardAPI,
				securityapi.FSTypeEmptyDir,
				securityapi.FSTypeFC,
				securityapi.FSTypeFlexVolume,
				securityapi.FSTypeFlocker,
				securityapi.FSTypeGCEPersistentDisk,
				securityapi.FSTypeGitRepo,
				securityapi.FSTypeGlusterfs,
				securityapi.FSTypeISCSI,
				securityapi.FSTypeNFS,
				securityapi.FSTypePersistentVolumeClaim,
				securityapi.FSTypeRBD,
				securityapi.FSTypeSecret}
			scc.Volumes = []securityapi.FSType{volumeTypes[c.Rand.Intn(len(volumeTypes))]}
		},
	)
	return f
}

func defaultHookContainerName(hook *oapps.LifecycleHook, containerName string) {
	if hook == nil {
		return
	}
	for i := range hook.TagImages {
		if len(hook.TagImages[i].ContainerName) == 0 {
			hook.TagImages[i].ContainerName = containerName
		}
	}
	if hook.ExecNewPod != nil {
		if len(hook.ExecNewPod.ContainerName) == 0 {
			hook.ExecNewPod.ContainerName = containerName
		}
	}
}

const fuzzIters = 20

// For debugging problems
func TestSpecificKind(t *testing.T) {
	seed := int64(2703387474910584091)
	fuzzer := originFuzzer(t, seed)

	legacyscheme.Scheme.Log(t)
	defer legacyscheme.Scheme.Log(nil)

	gvk := authorizationapi.SchemeGroupVersion.WithKind("ClusterRole")
	// TODO: make upstream CodecFactory customizable
	codecs := serializer.NewCodecFactory(legacyscheme.Scheme)
	for i := 0; i < fuzzIters; i++ {
		roundtrip.RoundTripSpecificKindWithoutProtobuf(t, gvk, legacyscheme.Scheme, codecs, fuzzer, nil)
	}
}

var dockerImageTypes = map[schema.GroupVersionKind]bool{
	image.SchemeGroupVersion.WithKind("DockerImage"): true,
}

// TestRoundTripTypes applies the round-trip test to all round-trippable Kinds
// in all of the API groups registered for test in the testapi package.
func TestRoundTripTypes(t *testing.T) {
	seed := rand.Int63()
	fuzzer := originFuzzer(t, seed)

	roundtrip.RoundTripTypes(t, legacyscheme.Scheme, legacyscheme.Codecs, fuzzer, dockerImageTypes)
}

// TestRoundTripDockerImage tests DockerImage whether it serializes from/into docker's registry API.
func TestRoundTripDockerImage(t *testing.T) {
	seed := rand.Int63()
	fuzzer := originFuzzer(t, seed)

	for gvk := range dockerImageTypes {
		roundtrip.RoundTripSpecificKindWithoutProtobuf(t, gvk, legacyscheme.Scheme, legacyscheme.Codecs, fuzzer, nil)
		roundtrip.RoundTripSpecificKindWithoutProtobuf(t, gvk, legacyscheme.Scheme, legacyscheme.Codecs, fuzzer, nil)
	}
}

func mergeGvks(a, b map[schema.GroupVersionKind]bool) map[schema.GroupVersionKind]bool {
	c := map[schema.GroupVersionKind]bool{}
	for k, v := range a {
		c[k] = v
	}
	for k, v := range b {
		c[k] = v
	}
	return c
}
