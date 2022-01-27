package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/howardjohn/kubeconfig-proxy/mux"
	"github.com/howardjohn/kubeconfig-proxy/third_party/kind/kubeconfig"
	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"k8s.io/kubectl/pkg/proxy"
)

func fail(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

var rootCmd = &cobra.Command{
	Use:   "kubeconfig-proxy",
	Short: "kubeconfig-proxy is a tool to open a persistent tunnel to an api server to improve kubectl speed",
}

func init() {
	rootCmd.AddCommand(serverCommand)
	serverCommand.PersistentFlags().StringVarP(&KubeConfig, "kubeconfig", "k", filepath.Join(homedir.HomeDir(), ".kube", "config"), "kubeconfig")
}

var serverCommand = &cobra.Command{
	Use:   "server",
	Short: "start proxy server",
	RunE: func(cmd *cobra.Command, args []string) error {
		rawCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: KubeConfig},
			&clientcmd.ConfigOverrides{}).RawConfig()
		if err != nil {
			return err
		}

		mux := mux.NewServeMux()
		for n, c := range rawCfg.Clusters {
			if !(strings.HasSuffix(n, "-kubeconfig-proxy") && strings.HasPrefix(c.Server, "http://127.0.0.1:64443")) {
				// Not a proxy cluster
				continue
			}
			name := strings.TrimSuffix(n, "-kubeconfig-proxy")
			cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
				&clientcmd.ClientConfigLoadingRules{ExplicitPath: KubeConfig},
				&clientcmd.ConfigOverrides{
					CurrentContext: name,
				}).ClientConfig()
			if err != nil {
				return fmt.Errorf("failed to connect to %v", name)
			}
			// Setup handler for each context, with prefix as the context
			apiProxyPrefix := "/" + name
			var filter *proxy.FilterServer = nil
			proxyHandler, err := proxy.NewProxyHandler(apiProxyPrefix, filter, cfg, 0, false)
			if err != nil {
				return err
			}
			mux.Handle(apiProxyPrefix, proxyHandler)
			mux.Handle(apiProxyPrefix + "/", proxyHandler)
			log.Println("Proxying server", name)
		}

		l, err := net.Listen("tcp", "127.0.0.1:64443")
		if err != nil {
			return err
		}
		log.Println("listening on ", l.Addr().String())
		// Have a big mux serving them
		if err := http.Serve(l, mux); err != nil {
			panic(err.Error())
		}
		return nil
	},
}

var KubeConfig string

func init() {
	rootCmd.AddCommand(proxyCommand)
	proxyCommand.PersistentFlags().StringVarP(&KubeConfig, "kubeconfig", "k", filepath.Join(homedir.HomeDir(), ".kube", "config"), "kubeconfig")
}

var proxyCommand = &cobra.Command{
	Use:   "proxy",
	Short: "register the current context to be proxied",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		ns, _, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: KubeConfig},
			&clientcmd.ConfigOverrides{}).Namespace()
		if err != nil {
			return err
		}
		cluster := name + "-kubeconfig-proxy"
		kcfg := &kubeconfig.Config{
			Clusters: []kubeconfig.NamedCluster{
				{
					Name: cluster,
					Cluster: kubeconfig.Cluster{
						Server: "http://127.0.0.1:64443/" + name,
					},
				},
			},
			Users: []kubeconfig.NamedUser{{
				Name: "proxy",
				User: nil,
			}},
			Contexts: []kubeconfig.NamedContext{{
				Name: cluster,
				Context: kubeconfig.Context{
					Cluster: cluster,
					User:    "proxy",
					OtherFields: map[string]interface{}{
						"namespace": ns,
					},
				},
			}},
			CurrentContext: cluster,
			OtherFields:    nil,
		}
		return kubeconfig.WriteMerged(kcfg, KubeConfig)
	},
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
