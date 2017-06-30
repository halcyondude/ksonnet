// Copyright 2017 The kubecfg authors
//
//
//    Licensed under the Apache License, Version 2.0 (the "License");
//    you may not use this file except in compliance with the License.
//    You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS,
//    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//    See the License for the specific language governing permissions and
//    limitations under the License.

package cmd

import (
	"bytes"
	"encoding/json"
	goflag "flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/golang/glog"
	"github.com/spf13/cobra"
	jsonnet "github.com/strickyak/jsonnet_cgo"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/pkg/api/unversioned"
	"k8s.io/client-go/pkg/runtime"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/ksonnet/kubecfg/utils"
)

const (
	flagJpath      = "jpath"
	flagExtVar     = "ext-str"
	flagExtVarFile = "ext-str-file"
	flagTlaVar     = "tla-str"
	flagTlaVarFile = "tla-str-file"
	flagResolver   = "resolve-images"
	flagResolvFail = "resolve-images-error"
)

var clientConfig clientcmd.ClientConfig

func init() {
	RootCmd.PersistentFlags().StringP(flagJpath, "J", "", "Additional jsonnet library search path")
	RootCmd.PersistentFlags().StringSliceP(flagExtVar, "V", nil, "Values of external variables")
	RootCmd.PersistentFlags().StringSlice(flagExtVarFile, nil, "Read external variable from a file")
	RootCmd.PersistentFlags().StringSliceP(flagTlaVar, "A", nil, "Values of top level arguments")
	RootCmd.PersistentFlags().StringSlice(flagTlaVarFile, nil, "Read top level argument from a file")
	RootCmd.PersistentFlags().String(flagResolver, "noop", "Change implementation of resolveImage native function. One of: noop, registry")
	RootCmd.PersistentFlags().String(flagResolvFail, "warn", "Action when resolveImage fails. One of ignore,warn,error")

	// The "usual" clientcmd/kubectl flags
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	loadingRules.DefaultClientConfig = &clientcmd.DefaultClientConfig
	overrides := clientcmd.ConfigOverrides{}
	kflags := clientcmd.RecommendedConfigOverrideFlags("")
	RootCmd.PersistentFlags().StringVar(&loadingRules.ExplicitPath, "kubeconfig", "", "Path to a kube config. Only required if out-of-cluster")
	clientcmd.BindOverrideFlags(&overrides, RootCmd.PersistentFlags(), kflags)
	clientConfig = clientcmd.NewInteractiveDeferredLoadingClientConfig(loadingRules, &overrides, os.Stdin)

	// Standard goflags (glog in particular)
	RootCmd.PersistentFlags().AddGoFlagSet(goflag.CommandLine)
	RootCmd.PersistentFlags().Set("logtostderr", "true")
}

// RootCmd is the root of cobra subcommand tree
var RootCmd = &cobra.Command{
	Use:           "kubecfg",
	Short:         "Synchronise Kubernetes resources with config files",
	SilenceErrors: true,
	SilenceUsage:  true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		goflag.CommandLine.Parse([]string{})
		glog.CopyStandardLogTo("INFO")
	},
}

// JsonnetVM constructs a new jsonnet.VM, according to command line
// flags
func JsonnetVM(cmd *cobra.Command) (*jsonnet.VM, error) {
	vm := jsonnet.Make()
	flags := cmd.Flags()

	jpath := os.Getenv("KUBECFG_JPATH")
	for _, p := range filepath.SplitList(jpath) {
		glog.V(2).Infoln("Adding jsonnet search path", p)
		vm.JpathAdd(p)
	}

	jpath, err := flags.GetString(flagJpath)
	if err != nil {
		return nil, err
	}
	for _, p := range filepath.SplitList(jpath) {
		glog.V(2).Infoln("Adding jsonnet search path", p)
		vm.JpathAdd(p)
	}

	extvars, err := flags.GetStringSlice(flagExtVar)
	if err != nil {
		return nil, err
	}
	for _, extvar := range extvars {
		kv := strings.SplitN(extvar, "=", 2)
		switch len(kv) {
		case 1:
			v, present := os.LookupEnv(kv[0])
			if present {
				vm.ExtVar(kv[0], v)
			} else {
				return nil, fmt.Errorf("Missing environment variable: %s", kv[0])
			}
		case 2:
			vm.ExtVar(kv[0], kv[1])
		}
	}

	extvarfiles, err := flags.GetStringSlice(flagExtVarFile)
	if err != nil {
		return nil, err
	}
	for _, extvar := range extvarfiles {
		kv := strings.SplitN(extvar, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("Failed to parse %s: missing '=' in %s", flagExtVarFile, extvar)
		}
		v, err := ioutil.ReadFile(kv[1])
		if err != nil {
			return nil, err
		}
		vm.ExtVar(kv[0], string(v))
	}

	tlavars, err := flags.GetStringSlice(flagTlaVar)
	if err != nil {
		return nil, err
	}
	for _, tlavar := range tlavars {
		kv := strings.SplitN(tlavar, "=", 2)
		switch len(kv) {
		case 1:
			v, present := os.LookupEnv(kv[0])
			if present {
				vm.TlaVar(kv[0], v)
			} else {
				return nil, fmt.Errorf("Missing environment variable: %s", kv[0])
			}
		case 2:
			vm.TlaVar(kv[0], kv[1])
		}
	}

	tlavarfiles, err := flags.GetStringSlice(flagTlaVarFile)
	if err != nil {
		return nil, err
	}
	for _, tlavar := range tlavarfiles {
		kv := strings.SplitN(tlavar, "=", 2)
		if len(kv) != 2 {
			return nil, fmt.Errorf("Failed to parse %s: missing '=' in %s", flagTlaVarFile, tlavar)
		}
		v, err := ioutil.ReadFile(kv[1])
		if err != nil {
			return nil, err
		}
		vm.TlaVar(kv[0], string(v))
	}

	resolver, err := buildResolver(cmd)
	if err != nil {
		return nil, err
	}
	utils.RegisterNativeFuncs(vm, resolver)

	return vm, nil
}

func buildResolver(cmd *cobra.Command) (utils.Resolver, error) {
	flags := cmd.Flags()
	resolver, err := flags.GetString(flagResolver)
	if err != nil {
		return nil, err
	}
	failAction, err := flags.GetString(flagResolvFail)
	if err != nil {
		return nil, err
	}

	ret := resolverErrorWrapper{}

	switch failAction {
	case "ignore":
		ret.OnErr = func(error) error { return nil }
	case "warn":
		ret.OnErr = func(err error) error {
			glog.Warning(err.Error())
			return nil
		}
	case "error":
		ret.OnErr = func(err error) error { return err }
	default:
		return nil, fmt.Errorf("Bad value for --%s: %s", flagResolvFail, failAction)
	}

	switch resolver {
	case "noop":
		ret.Inner = utils.NewIdentityResolver()
	case "registry":
		ret.Inner = utils.NewRegistryResolver(&http.Client{
			Transport: utils.NewAuthTransport(http.DefaultTransport),
		})
	default:
		return nil, fmt.Errorf("Bad value for --%s: %s", flagResolver, resolver)
	}

	return &ret, nil
}

type resolverErrorWrapper struct {
	Inner utils.Resolver
	OnErr func(error) error
}

func (r *resolverErrorWrapper) Resolve(image *utils.ImageName) error {
	err := r.Inner.Resolve(image)
	if err != nil {
		err = r.OnErr(err)
	}
	return err
}

func readObjs(cmd *cobra.Command, paths []string) ([]*runtime.Unstructured, error) {
	vm, err := JsonnetVM(cmd)
	if err != nil {
		return nil, err
	}
	defer vm.Destroy()

	res := []*runtime.Unstructured{}
	for _, path := range paths {
		objs, err := utils.Read(vm, path)
		if err != nil {
			return nil, fmt.Errorf("Error reading %s: %v", path, err)
		}
		res = append(res, utils.FlattenToV1(objs)...)
	}
	return res, nil
}

// For debugging
func dumpJSON(v interface{}) string {
	buf := bytes.NewBuffer(nil)
	enc := json.NewEncoder(buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err.Error()
	}
	return string(buf.Bytes())
}

func restClientPool(cmd *cobra.Command) (dynamic.ClientPool, discovery.DiscoveryInterface, error) {
	conf, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, nil, err
	}

	disco, err := discovery.NewDiscoveryClientForConfig(conf)
	if err != nil {
		return nil, nil, err
	}

	discoCache := utils.NewMemcachedDiscoveryClient(disco)
	mapper := discovery.NewDeferredDiscoveryRESTMapper(discoCache, dynamic.VersionInterfaces)
	pathresolver := dynamic.LegacyAPIPathResolverFunc

	pool := dynamic.NewClientPool(conf, mapper, pathresolver)
	return pool, discoCache, nil
}

func serverResourceForGroupVersionKind(disco discovery.DiscoveryInterface, gvk unversioned.GroupVersionKind) (*unversioned.APIResource, error) {
	resources, err := disco.ServerResourcesForGroupVersion(gvk.GroupVersion().String())
	if err != nil {
		return nil, err
	}

	for _, r := range resources.APIResources {
		if r.Kind == gvk.Kind {
			glog.V(4).Infof("Chose API '%s' for %s", r.Name, gvk)
			return &r, nil
		}
	}

	return nil, fmt.Errorf("Server is unable to handle %s", gvk)
}

func clientForResource(pool dynamic.ClientPool, disco discovery.DiscoveryInterface, obj *runtime.Unstructured, defNs string) (*dynamic.ResourceClient, error) {
	gvk := obj.GroupVersionKind()

	client, err := pool.ClientForGroupVersionKind(gvk)
	if err != nil {
		return nil, err
	}

	resource, err := serverResourceForGroupVersionKind(disco, gvk)
	if err != nil {
		return nil, err
	}

	namespace := obj.GetNamespace()
	if namespace == "" {
		namespace = defNs
	}

	glog.V(4).Infof("Fetching client for %s namespace=%s", resource, namespace)
	rc := client.Resource(resource, namespace)
	return rc, nil
}