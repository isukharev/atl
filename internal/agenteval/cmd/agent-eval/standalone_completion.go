package main

import (
	"fmt"
	"io"
	"strings"
)

type standaloneCompletionNode struct {
	path     []string
	summary  string
	children []string
}

func standaloneCompletionNodes() []standaloneCompletionNode {
	root := standaloneCommandTree()
	nodes := []standaloneCompletionNode{{path: nil, summary: root.Summary, children: standaloneChildNames(root)}}
	standaloneWalkDescriptors(root, nil, func(path []string, descriptor standaloneCommandDescriptor) {
		nodes = append(nodes, standaloneCompletionNode{path: append([]string(nil), path...), summary: descriptor.Summary, children: standaloneChildNames(descriptor)})
		if len(descriptor.Modes) != 0 {
			variantPath := append(append([]string(nil), path...), "--variant")
			nodes = append(nodes, standaloneCompletionNode{
				path: variantPath, summary: "select a documented compatibility variant",
				children: append([]string(nil), descriptor.Modes...),
			})
		}
	})
	return nodes
}

func standaloneChildNames(descriptor standaloneCommandDescriptor) []string {
	children := make([]string, 0, len(descriptor.Children))
	for _, child := range descriptor.Children {
		children = append(children, child.Name)
	}
	return children
}

func writeStandaloneCompletion(writer io.Writer, shell string) bool {
	nodes := standaloneCompletionNodes()
	switch shell {
	case "bash":
		writeStandaloneBashCompletion(writer, nodes)
	case "fish":
		writeStandaloneFishCompletion(writer, nodes)
	case "powershell":
		writeStandalonePowerShellCompletion(writer, nodes)
	case "zsh":
		writeStandaloneZshCompletion(writer, nodes)
	default:
		return false
	}
	return true
}

func writeStandaloneBashCompletion(writer io.Writer, nodes []standaloneCompletionNode) {
	fmt.Fprintln(writer, "_agent_eval_complete() {")
	fmt.Fprintln(writer, "  local cur path")
	fmt.Fprintln(writer, "  cur=\"${COMP_WORDS[COMP_CWORD]}\"")
	fmt.Fprintln(writer, "  path=\"${COMP_WORDS[*]:1:COMP_CWORD-1}\"")
	fmt.Fprintln(writer, "  case \"$path\" in")
	for _, node := range nodes {
		if len(node.children) == 0 {
			continue
		}
		fmt.Fprintf(writer, "    %q) COMPREPLY=( $(compgen -W %q -- \"$cur\") ) ;;\n", strings.Join(node.path, " "), strings.Join(node.children, " "))
	}
	fmt.Fprintln(writer, "    *) COMPREPLY=() ;;")
	fmt.Fprintln(writer, "  esac")
	fmt.Fprintln(writer, "}")
	fmt.Fprintln(writer, "complete -F _agent_eval_complete agent-eval")
}

func writeStandaloneFishCompletion(writer io.Writer, nodes []standaloneCompletionNode) {
	fmt.Fprintln(writer, "complete -c agent-eval -f")
	for _, node := range nodes {
		for _, child := range node.children {
			condition := "__fish_use_subcommand"
			if len(node.path) > 0 {
				condition = "test (string join ' ' (commandline -opc)) = 'agent-eval " + strings.Join(node.path, " ") + "'"
			}
			fmt.Fprintf(writer, "complete -c agent-eval -n %q -a %q\n", condition, child)
		}
	}
}

func writeStandalonePowerShellCompletion(writer io.Writer, nodes []standaloneCompletionNode) {
	fmt.Fprintln(writer, "Register-ArgumentCompleter -Native -CommandName agent-eval -ScriptBlock {")
	fmt.Fprintln(writer, "  param($wordToComplete, $commandAst, $cursorPosition)")
	fmt.Fprintln(writer, "  $arguments = @($commandAst.CommandElements | ForEach-Object { $_.Extent.Text } | Select-Object -Skip 1)")
	fmt.Fprintln(writer, "  if ($wordToComplete.Length -gt 0 -and $arguments.Count -gt 0) {")
	fmt.Fprintln(writer, "    if ($arguments.Count -eq 1) { $arguments = @() } else { $arguments = $arguments[0..($arguments.Count - 2)] }")
	fmt.Fprintln(writer, "  }")
	fmt.Fprintln(writer, "  $path = $arguments -join ' '")
	fmt.Fprintln(writer, "  $children = @()")
	fmt.Fprintln(writer, "  switch ($path) {")
	for _, node := range nodes {
		if len(node.children) == 0 {
			continue
		}
		fmt.Fprintf(writer, "    %q { $children = @(", strings.Join(node.path, " "))
		for index, child := range node.children {
			if index != 0 {
				fmt.Fprint(writer, ", ")
			}
			fmt.Fprintf(writer, "%q", child)
		}
		fmt.Fprintln(writer, "); break }")
	}
	fmt.Fprintln(writer, "    default { $children = @() }")
	fmt.Fprintln(writer, "  }")
	fmt.Fprintln(writer, "  $children | Where-Object { $_.StartsWith($wordToComplete, [System.StringComparison]::OrdinalIgnoreCase) } | ForEach-Object { [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_) }")
	fmt.Fprintln(writer, "}")
}

func writeStandaloneZshCompletion(writer io.Writer, nodes []standaloneCompletionNode) {
	fmt.Fprintln(writer, "#compdef agent-eval")
	fmt.Fprintln(writer, "_agent_eval() {")
	fmt.Fprintln(writer, "  local path=\"${(j: :)words[2,$((CURRENT - 1))]}\"")
	fmt.Fprintln(writer, "  local -a commands")
	fmt.Fprintln(writer, "  case \"$path\" in")
	for _, node := range nodes {
		if len(node.children) == 0 {
			continue
		}
		fmt.Fprintf(writer, "    %q)\n", strings.Join(node.path, " "))
		fmt.Fprintln(writer, "      commands=(")
		for _, child := range node.children {
			childPath := append(append([]string(nil), node.path...), child)
			fmt.Fprintf(writer, "        %q\n", child+":"+standaloneCompletionSummary(nodes, childPath))
		}
		fmt.Fprintln(writer, "      )")
		fmt.Fprintln(writer, "      ;;")
	}
	fmt.Fprintln(writer, "    *) commands=() ;;")
	fmt.Fprintln(writer, "  esac")
	fmt.Fprintln(writer, "  _describe 'command' commands")
	fmt.Fprintln(writer, "}")
	fmt.Fprintln(writer, "_agent_eval")
}

func standaloneCompletionSummary(nodes []standaloneCompletionNode, path []string) string {
	if len(path) >= 2 && path[len(path)-2] == "--variant" {
		return "select a documented compatibility variant"
	}
	for _, node := range nodes {
		if strings.Join(node.path, " ") == strings.Join(path, " ") {
			return node.summary
		}
	}
	return "agent-eval command"
}
