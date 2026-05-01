/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package cli implements the `trafficctl` operator CLI. It is a thin
// shell around controller-runtime's client: list, inspect, and toggle
// TrafficPolicy resources without hand-writing kubectl patches.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"

	trafficv1alpha1 "github.com/yusuf/trafficctl/api/v1alpha1"
)

// globals holds flags parsed on the root command so subcommands can read
// them. Not a struct pointer chain — Cobra's PersistentFlags write directly.
type globals struct {
	kubeconfig string
	namespace  string
	allNs      bool
}

// NewRootCommand builds the trafficctl command tree.
func NewRootCommand() *cobra.Command {
	g := &globals{}
	root := &cobra.Command{
		Use:          "trafficctl",
		Short:        "Operator CLI for the TrafficPolicy controller",
		SilenceUsage: true,
	}
	root.PersistentFlags().StringVar(&g.kubeconfig, "kubeconfig", "", "path to kubeconfig (defaults to $KUBECONFIG or ~/.kube/config)")
	root.PersistentFlags().StringVarP(&g.namespace, "namespace", "n", "", "namespace scope (defaults to current context)")
	root.PersistentFlags().BoolVarP(&g.allNs, "all-namespaces", "A", false, "operate across all namespaces (only valid for list)")

	root.AddCommand(newListCommand(g))
	root.AddCommand(newStatusCommand(g))
	root.AddCommand(newFreezeCommand(g))
	root.AddCommand(newResumeCommand(g))
	return root
}

func buildClient(g *globals) (ctrlclient.Client, string, error) {
	loader := clientcmd.NewDefaultClientConfigLoadingRules()
	if g.kubeconfig != "" {
		loader.ExplicitPath = g.kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loader, overrides)
	restCfg, err := cc.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load kubeconfig: %w", err)
	}
	ns := g.namespace
	if ns == "" {
		ns, _, err = cc.Namespace()
		if err != nil || ns == "" {
			ns = "default"
		}
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(trafficv1alpha1.AddToScheme(scheme))

	c, err := ctrlclient.New(restCfg, ctrlclient.Options{Scheme: scheme})
	if err != nil {
		return nil, "", fmt.Errorf("build client: %w", err)
	}
	return c, ns, nil
}

func newListCommand(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List TrafficPolicies in a namespace",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := buildClient(g)
			if err != nil {
				return err
			}
			return runList(cmd.Context(), c, ns, g.allNs, cmd.OutOrStdout())
		},
	}
}

func newStatusCommand(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "status NAME",
		Short: "Show detailed status for one TrafficPolicy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient(g)
			if err != nil {
				return err
			}
			return runStatus(cmd.Context(), c, ns, args[0], cmd.OutOrStdout())
		},
	}
}

func newFreezeCommand(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "freeze NAME",
		Short: "Set .spec.paused=true to freeze weights",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient(g)
			if err != nil {
				return err
			}
			return togglePaused(cmd.Context(), c, ns, args[0], true, cmd.OutOrStdout())
		},
	}
}

func newResumeCommand(g *globals) *cobra.Command {
	return &cobra.Command{
		Use:   "resume NAME",
		Short: "Set .spec.paused=false so the controller may move weights again",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, ns, err := buildClient(g)
			if err != nil {
				return err
			}
			return togglePaused(cmd.Context(), c, ns, args[0], false, cmd.OutOrStdout())
		},
	}
}

// runList prints a table of policies. Output layout stays stable so that
// downstream tooling (scripts, watchers) can depend on it.
func runList(ctx context.Context, c ctrlclient.Client, namespace string, allNs bool, out io.Writer) error {
	var list trafficv1alpha1.TrafficPolicyList
	opts := []ctrlclient.ListOption{}
	if !allNs {
		opts = append(opts, ctrlclient.InNamespace(namespace))
	}
	if err := c.List(ctx, &list, opts...); err != nil {
		return fmt.Errorf("list policies: %w", err)
	}

	sort.Slice(list.Items, func(i, j int) bool {
		if list.Items[i].Namespace != list.Items[j].Namespace {
			return list.Items[i].Namespace < list.Items[j].Namespace
		}
		return list.Items[i].Name < list.Items[j].Name
	})

	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if allNs {
		if _, err := fmt.Fprintln(tw, "NAMESPACE\tNAME\tROUTE\tPHASE\tPAUSED\tWEIGHTS"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(tw, "NAME\tROUTE\tPHASE\tPAUSED\tWEIGHTS"); err != nil {
			return err
		}
	}
	for _, p := range list.Items {
		weights := formatWeights(p.Status.Weights)
		phase := string(p.Status.Phase)
		if phase == "" {
			phase = "-"
		}
		if allNs {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\t%s\n", p.Namespace, p.Name, p.Spec.RouteName, phase, p.Spec.Paused, weights); err != nil {
				return err
			}
		} else {
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%s\n", p.Name, p.Spec.RouteName, phase, p.Spec.Paused, weights); err != nil {
				return err
			}
		}
	}
	return tw.Flush()
}

// runStatus prints per-policy detail: spec bounds, current weights,
// condition summary, and last-transition reason.
func runStatus(ctx context.Context, c ctrlclient.Client, namespace, name string, out io.Writer) error {
	var p trafficv1alpha1.TrafficPolicy
	if err := c.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: name}, &p); err != nil {
		return fmt.Errorf("get policy: %w", err)
	}

	if _, err := fmt.Fprintf(out, "Name:         %s\n", p.Name); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Namespace:    %s\n", p.Namespace); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Route:        %s\n", p.Spec.RouteName); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Paused:       %t\n", p.Spec.Paused); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "Phase:        %s\n", defaultString(string(p.Status.Phase), "-")); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "LastReason:   %s\n", defaultString(p.Status.LastTransitionReason, "-")); err != nil {
		return err
	}
	if p.Status.LastEvaluationTime != nil {
		if _, err := fmt.Fprintf(out, "LastEval:     %s\n", p.Status.LastEvaluationTime.Format("2006-01-02T15:04:05Z07:00")); err != nil {
			return err
		}
	}
	if p.Status.LastWeightChangeTime != nil {
		if _, err := fmt.Fprintf(out, "LastShift:    %s\n", p.Status.LastWeightChangeTime.Format("2006-01-02T15:04:05Z07:00")); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintln(out, "\nBackends:"); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "  NAME\tMIN\tMAX\tCURRENT"); err != nil {
		return err
	}
	currents := map[string]int32{}
	for _, w := range p.Status.Weights {
		currents[w.Name] = w.Weight
	}
	for _, b := range p.Spec.Backends {
		w := "-"
		if v, ok := currents[b.Name]; ok {
			w = fmt.Sprintf("%d", v)
		}
		if _, err := fmt.Fprintf(tw, "  %s\t%d\t%d\t%s\n", b.Name, b.MinWeight, b.MaxWeight, w); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if len(p.Status.Conditions) > 0 {
		if _, err := fmt.Fprintln(out, "\nConditions:"); err != nil {
			return err
		}
		ctw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		if _, err := fmt.Fprintln(ctw, "  TYPE\tSTATUS\tREASON\tMESSAGE"); err != nil {
			return err
		}
		for _, cond := range p.Status.Conditions {
			if _, err := fmt.Fprintf(ctw, "  %s\t%s\t%s\t%s\n", cond.Type, cond.Status, cond.Reason, cond.Message); err != nil {
				return err
			}
		}
		if err := ctw.Flush(); err != nil {
			return err
		}
		ready := meta.FindStatusCondition(p.Status.Conditions, "Ready")
		if ready != nil && ready.Status != "True" {
			if _, err := fmt.Fprintf(out, "\nNot ready: %s - %s\n", ready.Reason, ready.Message); err != nil {
				return err
			}
		}
	}
	return nil
}

// togglePaused flips .spec.paused for the named policy. Uses a merge
// patch so unrelated in-flight changes to the spec aren't stomped.
func togglePaused(ctx context.Context, c ctrlclient.Client, namespace, name string, paused bool, out io.Writer) error {
	var p trafficv1alpha1.TrafficPolicy
	if err := c.Get(ctx, ctrlclient.ObjectKey{Namespace: namespace, Name: name}, &p); err != nil {
		return fmt.Errorf("get policy: %w", err)
	}
	if p.Spec.Paused == paused {
		if _, err := fmt.Fprintf(out, "%s/%s already paused=%t; nothing to do\n", namespace, name, paused); err != nil {
			return err
		}
		return nil
	}
	original := p.DeepCopy()
	p.Spec.Paused = paused
	if err := c.Patch(ctx, &p, ctrlclient.MergeFrom(original)); err != nil {
		return fmt.Errorf("patch policy: %w", err)
	}
	verb := "resumed"
	if paused {
		verb = "frozen"
	}
	if _, err := fmt.Fprintf(out, "%s/%s %s\n", namespace, name, verb); err != nil {
		return err
	}
	return nil
}

func formatWeights(ws []trafficv1alpha1.BackendWeight) string {
	if len(ws) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(ws))
	for _, w := range ws {
		parts = append(parts, fmt.Sprintf("%s=%d", w.Name, w.Weight))
	}
	return strings.Join(parts, ",")
}

func defaultString(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// Exit is exposed for tests that drive NewRootCommand directly.
var Exit = os.Exit
