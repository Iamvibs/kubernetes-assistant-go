package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"

	"github.com/janeczku/go-spinner"
	"github.com/manifoldco/promptui"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	flag "github.com/spf13/pflag"
	"github.com/walles/env"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

const (
	apply     = "Apply"
	dontApply = "Don't Apply"
	reprompt  = "Reprompt"
)

var (
	openaiAPIURLv1        = "https://api.openai.com/v1"             // The URL for the OpenAI API version 1.
	version               = "dev"                                   // The version of the Kubernetes Assistant CLI.
	kubernetesConfigFlags = genericclioptions.NewConfigFlags(false) // Flags for configuring the Kubernetes client.

	openAIDeploymentName = flag.String("openai-deployment-name", env.GetOr("OPENAI_DEPLOYMENT_NAME", env.String, "gpt-3.5-turbo-0301"), "The deployment name used for the model in OpenAI service.")                                                                                               // The name of the deployment used for the OpenAI model.
	openAIAPIKey         = flag.String("openai-api-key", env.GetOr("OPENAI_API_KEY", env.String, ""), "The API key for the OpenAI service. This is required.")                                                                                                                                     // The API key for the OpenAI service.
	openAIEndpoint       = flag.String("openai-endpoint", env.GetOr("OPENAI_ENDPOINT", env.String, openaiAPIURLv1), "The endpoint for OpenAI service. Defaults to"+openaiAPIURLv1+". Set this to your Local AI endpoint or Azure OpenAI Service, if needed.")                                      // The endpoint for the OpenAI service.
	azureModelMap        = flag.StringToString("azure-openai-map", env.GetOr("AZURE_OPENAI_MAP", env.Map(env.String, "=", env.String, ""), map[string]string{}), "The mapping from OpenAI model to Azure OpenAI deployment. Defaults to empty map. Example format: gpt-3.5-turbo=my-deployment.")  // The mapping from OpenAI model to Azure OpenAI deployment.
	requireConfirmation  = flag.Bool("require-confirmation", env.GetOr("REQUIRE_CONFIRMATION", strconv.ParseBool, true), "Whether to require confirmation before executing the command. Defaults to true.")                                                                                        // Whether to require confirmation before executing the command.
	temperature          = flag.Float64("temperature", env.GetOr("TEMPERATURE", env.WithBitSize(strconv.ParseFloat, 64), 0.0), "The temperature to use for the model. Range is between 0 and 1. Set closer to 0 if your want output to be more deterministic but less creative. Defaults to 0.0.") // The temperature to use for the model.
	raw                  = flag.Bool("raw", false, "Prints the raw YAML output immediately. Defaults to false.")                                                                                                                                                                                   // Whether to print the raw YAML output immediately.
	usek8sAPI            = flag.Bool("use-k8s-api", env.GetOr("USE_K8S_API", strconv.ParseBool, false), "Whether to use the Kubernetes API to create resources with function calling. Defaults to false.")                                                                                         // Whether to use the Kubernetes API to create resources with function calling.
	k8sOpenAPIURL        = flag.String("k8s-openapi-url", env.GetOr("K8S_OPENAPI_URL", env.String, ""), "The URL to a Kubernetes OpenAPI spec. Only used if use-k8s-api flag is true.")                                                                                                            // The URL to a Kubernetes OpenAPI spec.
	debug                = flag.Bool("debug", env.GetOr("DEBUG", strconv.ParseBool, false), "Whether to print debug logs. Defaults to false.")                                                                                                                                                     // Whether to print debug logs.
)

// InitAndExecute initializes the application and executes the root command.
// It checks if the OpenAI key is provided and exits if it is not.
// It then executes the root command.
func InitAndExecute() {
	if *openAIAPIKey == "" {
		fmt.Println("Please provide an OpenAI key.")
		os.Exit(1)
	}

	if err := RootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// RootCmd returns the root command for the kubectl-assistant CLI.
// It sets up the command with the necessary flags, pre-run actions, and the main run function.
func RootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "kubectl-assistant",
		Short:        "kubectl-assistant",
		Long:         "kubectl-assistant is a plugin for kubectl that allows you to interact with OpenAI GPT API.",
		Version:      version,
		SilenceUsage: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			// Set the log level to debug if the debug flag is enabled
			if *debug {
				log.SetLevel(log.DebugLevel)
				printDebugFlags()
			}
		},
		RunE: func(_ *cobra.Command, args []string) error {
			// Check if a prompt is provided
			if len(args) == 0 {
				return fmt.Errorf("prompt must be provided")
			}

			// Run the main logic of the CLI
			err := run(args)
			if err != nil {
				return err
			}

			return nil
		},
	}

	// Add Kubernetes configuration flags to the command
	kubernetesConfigFlags.AddFlags(cmd.PersistentFlags())

	return cmd
}

func printDebugFlags() {
	log.Debugf("openai-endpoint: %s", *openAIEndpoint)
	log.Debugf("openai-deployment-name: %s", *openAIDeploymentName)
	log.Debugf("azure-openai-map: %s", *azureModelMap)
	log.Debugf("temperature: %f", *temperature)
	log.Debugf("use-k8s-api: %t", *usek8sAPI)
	log.Debugf("k8s-openapi-url: %s", *k8sOpenAPIURL)
}

// run is the main function that executes the CLI command.
// It takes a slice of arguments and returns an error if any.
func run(args []string) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Create new OAI clients
	oaiClients, err := newOAIClients()
	if err != nil {
		return err
	}

	var action, completion string
	for action != apply {
		args = append(args, action)

		// Create a spinner to show processing status
		s := spinner.NewSpinner("Processing...")
		if !*debug && !*raw {
			s.SetCharset([]string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"})
			s.Start()
		}

		// Get GPT completion for the given arguments
		completion, err = gptCompletion(ctx, oaiClients, args, *openAIDeploymentName)
		if err != nil {
			return err
		}

		s.Stop()

		if *raw {
			fmt.Println(completion)
			return nil
		}

		// Print the manifest to be applied
		text := fmt.Sprintf("✨ Attempting to apply the following manifest:\n%s", completion)
		fmt.Println(text)

		// Prompt user for action
		action, err = userActionPrompt()
		if err != nil {
			return err
		}

		if action == dontApply {
			return nil
		}
	}

	// Apply the manifest
	return applyManifest(completion)
}

// userActionPrompt prompts the user for an action and returns the selected action.
// If requireConfirmation is not set, it immediately returns the "apply" action.
// Otherwise, it presents a prompt to the user with options to apply or not apply.
// The selected action is returned as a string.
// If an error occurs during the prompt, it returns the "dontApply" action and the error.
func userActionPrompt() (string, error) {
	if !*requireConfirmation {
		return apply, nil
	}

	var result string
	var err error
	items := []string{apply, dontApply}
	currentContext, err := getCurrentContextName()
	label := fmt.Sprintf("Would you like to apply this? [%[1]s/%[2]s/%[3]s]", reprompt, apply, dontApply)
	if err == nil {
		label = fmt.Sprintf("(context: %[1]s) %[2]s", currentContext, label)
	}

	prompt := promptui.SelectWithAdd{
		Label:    label,
		Items:    items,
		AddLabel: reprompt,
	}
	_, result, err = prompt.Run()
	if err != nil {
		return dontApply, err
	}

	return result, nil
}
