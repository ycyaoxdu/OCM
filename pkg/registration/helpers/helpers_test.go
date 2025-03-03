package helpers

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/openshift/library-go/pkg/operator/events/eventstesting"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakekube "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	clusterv1 "open-cluster-management.io/api/cluster/v1"

	testingcommon "open-cluster-management.io/ocm/pkg/common/testing"
	testinghelpers "open-cluster-management.io/ocm/pkg/registration/helpers/testing"
)

func TestIsValidHTTPSURL(t *testing.T) {
	cases := []struct {
		name      string
		serverURL string
		isValid   bool
	}{
		{
			name:      "an empty url",
			serverURL: "",
			isValid:   false,
		},
		{
			name:      "an invalid url",
			serverURL: "/path/path/path",
			isValid:   false,
		},
		{
			name:      "a http url",
			serverURL: "http://127.0.0.1:8080",
			isValid:   false,
		},
		{
			name:      "a https url",
			serverURL: "https://127.0.0.1:6443",
			isValid:   true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			isValid := IsValidHTTPSURL(c.serverURL)
			if isValid != c.isValid {
				t.Errorf("expected %t, but %t", c.isValid, isValid)
			}
		})
	}
}

func TestCleanUpManagedClusterManifests(t *testing.T) {
	applyFiles := map[string]runtime.Object{
		"namespace":          testinghelpers.NewUnstructuredObj("v1", "Namespace", "", "n1"),
		"clusterrole":        testinghelpers.NewUnstructuredObj("rbac.authorization.k8s.io/v1", "ClusterRole", "", "cr1"),
		"clusterrolebinding": testinghelpers.NewUnstructuredObj("rbac.authorization.k8s.io/v1", "ClusterRoleBinding", "", "crb1"),
		"role":               testinghelpers.NewUnstructuredObj("rbac.authorization.k8s.io/v1", "Role", "n1", "r1"),
		"rolebinding":        testinghelpers.NewUnstructuredObj("rbac.authorization.k8s.io/v1", "RoleBinding", "n1", "rb1"),
	}
	expectedActions := []string{}
	for i := 0; i < len(applyFiles); i++ {
		expectedActions = append(expectedActions, "delete")
	}
	cases := []struct {
		name            string
		applyObject     []runtime.Object
		applyFiles      map[string]runtime.Object
		validateActions func(t *testing.T, actions []clienttesting.Action)
		expectedErr     string
	}{
		{
			name: "delete applied objects",
			applyObject: []runtime.Object{
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "n1"}},
				&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "cr1"}},
				&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "crb1"}},
				&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "r1", Namespace: "n1"}},
				&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "rb1", Namespace: "n1"}},
			},
			applyFiles: applyFiles,
			validateActions: func(t *testing.T, actions []clienttesting.Action) {
				testingcommon.AssertActions(t, actions, expectedActions...)
			},
		},
		{
			name:        "there are no applied objects",
			applyObject: []runtime.Object{},
			applyFiles:  applyFiles,
			validateActions: func(t *testing.T, actions []clienttesting.Action) {
				testingcommon.AssertActions(t, actions, expectedActions...)
			},
		},
		{
			name:            "unhandled types",
			applyObject:     []runtime.Object{},
			applyFiles:      map[string]runtime.Object{"secret": testinghelpers.NewUnstructuredObj("v1", "Secret", "n1", "s1")},
			expectedErr:     "unhandled type *v1.Secret",
			validateActions: testingcommon.AssertNoActions,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kubeClient := fakekube.NewSimpleClientset(c.applyObject...)
			cleanUpErr := CleanUpManagedClusterManifests(
				context.TODO(),
				kubeClient,
				eventstesting.NewTestingEventRecorder(t),
				func(name string) ([]byte, error) {
					if c.applyFiles[name] == nil {
						return nil, fmt.Errorf("Failed to find file")
					}
					return json.Marshal(c.applyFiles[name])
				},
				getApplyFileNames(c.applyFiles)...,
			)
			testingcommon.AssertError(t, cleanUpErr, c.expectedErr)
			c.validateActions(t, kubeClient.Actions())
		})
	}
}

func TestFindTaintByKey(t *testing.T) {
	cases := []struct {
		name     string
		cluster  *clusterv1.ManagedCluster
		key      string
		expected *clusterv1.Taint
	}{
		{
			name: "nil of managed cluster",
			key:  "taint1",
		},
		{
			name: "taint found",
			cluster: &clusterv1.ManagedCluster{
				Spec: clusterv1.ManagedClusterSpec{
					Taints: []clusterv1.Taint{
						{
							Key:   "taint1",
							Value: "value1",
						},
					},
				},
			},
			key: "taint1",
			expected: &clusterv1.Taint{
				Key:   "taint1",
				Value: "value1",
			},
		},
		{
			name: "taint not found",
			cluster: &clusterv1.ManagedCluster{
				Spec: clusterv1.ManagedClusterSpec{
					Taints: []clusterv1.Taint{
						{
							Key:   "taint1",
							Value: "value1",
						},
					},
				},
			},
			key: "taint2",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			actual := FindTaintByKey(c.cluster, c.key)
			if !reflect.DeepEqual(actual, c.expected) {
				t.Errorf("expected %v but got %v", c.expected, actual)
			}
		})
	}
}

func getApplyFileNames(applyFiles map[string]runtime.Object) []string {
	keys := []string{}
	for key := range applyFiles {
		keys = append(keys, key)
	}
	return keys
}

var (
	UnavailableTaint = clusterv1.Taint{
		Key:    clusterv1.ManagedClusterTaintUnavailable,
		Effect: clusterv1.TaintEffectNoSelect,
	}

	UnreachableTaint = clusterv1.Taint{
		Key:    clusterv1.ManagedClusterTaintUnreachable,
		Effect: clusterv1.TaintEffectNoSelect,
	}
)

func TestIsTaintsEqual(t *testing.T) {
	cases := []struct {
		name    string
		taints1 []clusterv1.Taint
		taints2 []clusterv1.Taint
		expect  bool
	}{
		{
			name:    "two empty taints",
			taints1: []clusterv1.Taint{},
			taints2: []clusterv1.Taint{},
			expect:  true,
		},
		{
			name:    "two nil taints",
			taints1: nil,
			taints2: nil,
			expect:  true,
		},
		{
			name:    "len(taints1) = 1, len(taints2) = 0",
			taints1: []clusterv1.Taint{UnavailableTaint},
			taints2: []clusterv1.Taint{},
			expect:  false,
		},
		{
			name:    "taints1 is the same as taints",
			taints1: []clusterv1.Taint{UnreachableTaint},
			taints2: []clusterv1.Taint{UnreachableTaint},
			expect:  true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			actual := reflect.DeepEqual(c.taints1, c.taints2)
			if actual != c.expect {
				t.Errorf("expected %t, but %t", c.expect, actual)
			}
		})
	}
}

func TestAddTaints(t *testing.T) {
	cases := []struct {
		name          string
		taints        []clusterv1.Taint
		addTaint      clusterv1.Taint
		resTaints     []clusterv1.Taint
		expectUpdated bool
	}{
		{
			name:          "add taint success",
			taints:        []clusterv1.Taint{},
			addTaint:      UnreachableTaint,
			expectUpdated: true,
			resTaints:     []clusterv1.Taint{UnreachableTaint},
		},
		{
			name:          "add taint fail, taint already exists",
			taints:        []clusterv1.Taint{UnreachableTaint},
			addTaint:      UnreachableTaint,
			expectUpdated: false,
			resTaints:     []clusterv1.Taint{UnreachableTaint},
		},
		{
			name:          "nil pointer judgment",
			taints:        nil,
			addTaint:      UnreachableTaint,
			expectUpdated: true,
			resTaints:     []clusterv1.Taint{UnreachableTaint},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			updated := AddTaints(&c.taints, c.addTaint)
			if updated != c.expectUpdated {
				t.Errorf("updated expected %t, but %t", c.expectUpdated, updated)
			}
			if !reflect.DeepEqual(c.taints, c.resTaints) {
				t.Errorf("taints expected %+v, but %+v", c.taints, c.resTaints)
			}
		})
	}
}

func TestRemoveTaints(t *testing.T) {
	cases := []struct {
		name          string
		taints        []clusterv1.Taint
		removeTaint   clusterv1.Taint
		resTaints     []clusterv1.Taint
		expectUpdated bool
	}{
		{
			name:          "nil pointer judgment",
			taints:        nil,
			removeTaint:   UnreachableTaint,
			expectUpdated: false,
			resTaints:     nil,
		},
		{
			name:          "remove success",
			taints:        []clusterv1.Taint{UnreachableTaint},
			removeTaint:   UnreachableTaint,
			expectUpdated: true,
			resTaints:     []clusterv1.Taint{},
		},
		{
			name:          "remove taint failed, taint not exists",
			taints:        []clusterv1.Taint{UnreachableTaint},
			removeTaint:   UnavailableTaint,
			expectUpdated: false,
			resTaints:     []clusterv1.Taint{UnreachableTaint},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			updated := RemoveTaints(&c.taints, c.removeTaint)
			if updated != c.expectUpdated {
				t.Errorf("updated expected %t, but %t", c.expectUpdated, updated)
			}
			if !reflect.DeepEqual(c.taints, c.resTaints) {
				t.Errorf("taints expected %+v, but %+v", c.taints, c.resTaints)
			}
		})
	}
}
