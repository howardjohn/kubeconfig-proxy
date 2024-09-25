package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

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

type MuxSwap struct {
	mu  sync.RWMutex
	mux *http.ServeMux
}

func (m *MuxSwap) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	m.mu.RLock()
	mux := m.mux
	m.mu.RUnlock()
	mux.ServeHTTP(writer, request)
}

const (
	UdsPath = "@/kubeconfig-proxy"
)

var serverCommand = &cobra.Command{
	Use:   "server",
	Short: "start proxy server",
	RunE: func(cmd *cobra.Command, args []string) error {
		mux, err := loadMux()
		if err != nil {
			return err
		}

		muxHolder := &MuxSwap{
			mux: mux,
		}
		managementListener, err := net.Listen("unix", UdsPath)
		if err != nil {
			return err
		}
		defer managementListener.Close()
		go http.Serve(managementListener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				mux, err := loadMux()
				if err != nil {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
				muxHolder.mu.Lock()
				muxHolder.mux = mux
				muxHolder.mu.Unlock()
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
		}))

		l, err := net.Listen("tcp", "127.0.0.1:64443")
		if err != nil {
			return err
		}
		log.Println("listening on ", l.Addr().String())
		// Have a big mux serving them
		if err := http.Serve(l, muxHolder); err != nil {
			panic(err.Error())
		}
		return nil
	},
}

func loadMux() (*http.ServeMux, error) {
	rawCfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: KubeConfig},
		&clientcmd.ConfigOverrides{}).RawConfig()
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	log.Println("Loading proxy clusters...")
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
			return nil, fmt.Errorf("failed to connect to %v", name)
		}
		// Setup handler for each context, with prefix as the context
		apiProxyPrefix := "/" + name
		var filter *proxy.FilterServer = nil
		proxyHandler, err := proxy.NewProxyHandler(apiProxyPrefix, filter, cfg, 0, false)
		if err != nil {
			return nil, err
		}
		mux.Handle(apiProxyPrefix, proxyHandler)
		mux.Handle(apiProxyPrefix+"/", proxyHandler)
		log.Println("Proxying kubeconfig:", name)
	}
	return mux, nil
}

var KubeConfig string

func init() {
	rootCmd.AddCommand(proxyCommand)
	proxyCommand.PersistentFlags().StringVarP(&KubeConfig, "kubeconfig", "k", filepath.Join(homedir.HomeDir(), ".kube", "config"), "kubeconfig")
}

var proxyCommand = &cobra.Command{
	Use:   "proxy",
	Short: "register the current context to be proxied",
	Example: `
# Translate the current context to point to the proxy
kubeconfig-proxy proxy

# Translate a specific context to point to the proxy
kubeconfig-proxy proxy kind-kind
`,
	RunE: func(cmd *cobra.Command, args []string) error {
		cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: KubeConfig},
			&clientcmd.ConfigOverrides{})
		var name string
		if len(args) > 1 {
			return fmt.Errorf("expected 0 or 1 args")
		} else if len(args) == 1 {
			name = args[0]
		} else {
			raw, err := cc.RawConfig()
			if err != nil {
				return fmt.Errorf("failed to load current context: %v", err)
			}
			name = raw.CurrentContext
		}
		ns, _, err := cc.Namespace()
		if err != nil {
			return err
		}

		if strings.HasSuffix(name, "-kubeconfig-proxy") {
			return fmt.Errorf("this context is already registered")
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

		// Merge into their kubeconfig
		if err := kubeconfig.WriteMerged(kcfg, KubeConfig); err != nil {
			return err
		}

		// Tell our proxy to start handling this
		client := http.Client{}
		client.Transport = &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", UdsPath)
			},
		}
		req, err := http.NewRequest("POST", "http://local/", nil)
		if err != nil {
			return err
		}
		if _, err := client.Do(req); err != nil {
			return err
		}
		return nil
	},
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
