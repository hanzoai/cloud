import { CodeView } from "@/src/components/ui/CodeJsonViewer";
import {
  Tabs,
  TabsList,
  TabsContent,
  TabsTrigger,
} from "@/src/components/ui/tabs";
import { useUiCustomization } from "@/src/features/ui-customization/useUiCustomization";
import { env } from "@/src/env.mjs";
import { usePostHogClientCapture } from "@/src/features/posthog-analytics/usePostHogClientCapture";
import Link from "next/link";

export const QuickstartExamples = ({
  secretKey,
  publicKey,
}: {
  secretKey: string;
  publicKey: string;
}) => {
  const uiCustomization = useUiCustomization();
  const capture = usePostHogClientCapture();
  const tabs = [
    { value: "python", label: "Python" },
    { value: "js", label: "JS/TS" },
    { value: "openai", label: "OpenAI" },
    { value: "langchain", label: "Langchain" },
    { value: "langchain-js", label: "Langchain JS" },
    { value: "llamaindex", label: "LlamaIndex" },
    { value: "other", label: "Other" },
  ];
  const host = `${uiCustomization?.hostname ?? window.origin}${env.NEXT_PUBLIC_BASE_PATH ?? ""}`;

  // if custom docs link, do not show quickstart examples but refer to docs
  if (uiCustomization?.documentationHref) {
    return (
      <p className="mb-2">
        See your{" "}
        <Link
          href={uiCustomization.documentationHref}
          target="_blank"
          className="underline"
        >
          internal documentation
        </Link>{" "}
        for details on how to set up Hanzo Cloud in your organization.
      </p>
    );
  }

  return (
    <div>
      <Tabs defaultValue="python" className="relative max-w-full">
        <div className="overflow-x-scroll">
          <TabsList>
            {tabs.map((tab) => (
              <TabsTrigger
                key={tab.value}
                value={tab.value}
                onClick={() =>
                  capture("onboarding:code_example_tab_switch", {
                    tabLabel: tab.value,
                  })
                }
              >
                {tab.label}
              </TabsTrigger>
            ))}
          </TabsList>
        </div>
        <TabsContent value="python">
          <CodeView content="pip install hanzo" className="mb-2" />
          <CodeView
            content={`from hanzo import HanzoCloud\n\nhanzo = HanzoCloud(\n  secret_key="${secretKey}",\n  public_key="${publicKey}",\n  host="${host}"\n)`}
          />
          <p className="mt-3 text-xs text-muted-foreground">
            See{" "}
            <a
              href="https://hanzo.ai/docs/get-started"
              className="underline"
              target="_blank"
              rel="noopener noreferrer"
            >
              Quickstart
            </a>{" "}
            and{" "}
            <a
              href="https://hanzo.ai/docs/sdk/python"
              className="underline"
              target="_blank"
              rel="noopener noreferrer"
            >
              Python docs
            </a>{" "}
            for more details and an end-to-end example.
          </p>
        </TabsContent>
        <TabsContent value="js">
          <CodeView content="npm install hanzo" className="mb-2" />
          <CodeView
            content={`import { Hanzo } from "hanzo";\n\nconst hanzo = new Hanzo({\n  secretKey: "${secretKey}",\n  publicKey: "${publicKey}",\n  baseUrl: "${host}"\n});`}
          />
          <p className="mt-3 text-xs text-muted-foreground">
            See{" "}
            <a
              href="https://hanzo.ai/docs/get-started"
              className="underline"
              target="_blank"
              rel="noopener noreferrer"
            >
              Quickstart
            </a>{" "}
            and{" "}
            <a
              href="https://hanzo.ai/docs/sdk/typescript"
              className="underline"
              target="_blank"
              rel="noopener noreferrer"
            >
              JS/TS docs
            </a>{" "}
            for more details and an end-to-end example.
          </p>
        </TabsContent>
        <TabsContent value="openai">
          <p className="mt-2 text-xs text-muted-foreground">
            The integration is a drop-in replacement for the OpenAI Python SDK.
            By changing the import, Hanzo Cloud will capture all LLM calls and send
            them to Hanzo Cloud asynchronously.
          </p>
          <CodeView content="pip install hanzo" className="my-2" />
          <CodeView
            title=".env"
            content={`HANZO_SECRET_KEY=${secretKey}\nHANZO_PUBLIC_KEY=${publicKey}\nHANZO_HOST="${host}"`}
            className="my-2"
          />
          <CodeView
            content={`# remove: import openai\n\nfrom hanzo.openai import openai`}
            className="my-2"
          />
          <p className="mt-2 text-xs text-muted-foreground">
            Use the OpenAI SDK as you would normally. See the{" "}
            <a
              href="https://hanzo.ai/docs/integrations/openai"
              className="underline"
              target="_blank"
              rel="noopener noreferrer"
            >
              OpenAI Integration docs
            </a>{" "}
            for more details and an end-to-end example.
          </p>
        </TabsContent>
        <TabsContent value="langchain">
          <p className="mt-2 text-xs text-muted-foreground">
            The integration uses the Langchain callback system to automatically
            capture detailed traces of your Langchain executions.
          </p>
          <CodeView content="pip install hanzo" className="my-2" />
          <CodeView
            content={LANGCHAIN_PYTHON_CODE({ publicKey, secretKey, host })}
            className="my-2"
          />
          <p className="mt-2 text-xs text-muted-foreground">
            See the{" "}
            <a
              href="https://hanzo.ai/docs/integrations/langchain/python"
              className="underline"
              target="_blank"
              rel="noopener noreferrer"
            >
              Langchain Integration docs
            </a>{" "}
            for more details and an end-to-end example.
          </p>
        </TabsContent>
        <TabsContent value="langchain-js">
          <p className="mt-2 text-xs text-muted-foreground">
            The integration uses the Langchain callback system to automatically
            capture detailed traces of your Langchain executions.
          </p>
          <CodeView content="npm install @hanzo/hanzo-langchain" className="my-2" />
          <CodeView
            content={LANGCHAIN_JS_CODE({ publicKey, secretKey, host })}
            className="my-2"
          />
          <p className="mt-2 text-xs text-muted-foreground">
            See the{" "}
            <a
              href="https://hanzo.ai/docs/integrations/langchain/typescript"
              className="underline"
              target="_blank"
              rel="noopener noreferrer"
            >
              Langchain Integration docs
            </a>{" "}
            for more details and an end-to-end example.
          </p>
        </TabsContent>
        <TabsContent value="llamaindex">
          <p className="mt-2 text-xs text-muted-foreground">
            The integration uses the LlamaIndex callback system to automatically
            capture detailed traces of your LlamaIndex executions.
          </p>
          <CodeView
            content="pip install hanzo llama-index"
            className="my-2"
          />
          <CodeView
            content={LLAMA_INDEX_CODE({ publicKey, secretKey, host })}
            className="my-2"
          />
          <p className="mt-2 text-xs text-muted-foreground">
            See the{" "}
            <a
              href="https://hanzo.ai/docs/integrations/llama-index"
              className="underline"
              target="_blank"
              rel="noopener noreferrer"
            >
              LlamaIndex Integration docs
            </a>{" "}
            for more details and an end-to-end example.
          </p>
        </TabsContent>
        <TabsContent value="other">
          <p className="mt-2 text-xs text-muted-foreground">
            Use the{" "}
            <a
              href="https://api.reference.hanzo.ai/"
              className="underline"
              target="_blank"
              rel="noopener noreferrer"
            >
              API
            </a>{" "}
            or one of the{" "}
            <a
              href="https://hanzo.ai/docs/integrations"
              className="underline"
              target="_blank"
              rel="noopener noreferrer"
            >
              native integrations
            </a>{" "}
            (e.g. LiteLLM, Flowise, and Langflow) to integrate with HanzoCloud.
          </p>
        </TabsContent>
      </Tabs>
      <span className="mt-4 text-xs text-muted-foreground">
        Do you have questions or issues? Check out this{" "}
        <a
          href="https://hanzo.ai/faq/all/missing-traces"
          className="underline"
          target="_blank"
          rel="noopener noreferrer"
        >
          FAQ post
        </a>{" "}
        for common resolutions,{" "}
        <Link
          className="underline"
          href="https://hanzo.ai/docs/ask-ai"
          target="_blank"
          rel="noopener noreferrer"
        >
          Ask AI
        </Link>{" "}
        or{" "}
        <Link
          className="underline"
          href="https://hanzo.ai/support"
          target="_blank"
          rel="noopener noreferrer"
        >
          get support
        </Link>
        .
      </span>
    </div>
  );
};
const LANGCHAIN_PYTHON_CODE = (p: {
  publicKey: string;
  secretKey: string;
  host: string;
}) => `from hanzo.callback import CallbackHandler
hanzo_handler = CallbackHandler(
    public_key="${p.publicKey}",
    secret_key="${p.secretKey}",
    host="${p.host}"
)

# <Your Langchain code here>
 
# Add handler to run/invoke/call/chat
chain.invoke({"input": "<user_input>"}, config={"callbacks": [hanzo_handler]})`;

const LANGCHAIN_JS_CODE = (p: {
  publicKey: string;
  secretKey: string;
  host: string;
}) => `import { CallbackHandler } from "@hanzo/hanzo-langchain";
 
// Initialize Hanzo Cloud callback handler
const hanzoHandler = new CallbackHandler({
  publicKey: "${p.publicKey}",
  secretKey: "${p.secretKey}",
  baseUrl: "${p.host}"
});
 
// Your Langchain implementation
const chain = new LLMChain(...);
 
// Add handler as callback when running the Langchain agent
await chain.invoke(
  { input: "<user_input>" },
  { callbacks: [hanzoHandler] }
);`;

const LLAMA_INDEX_CODE = (p: {
  publicKey: string;
  secretKey: string;
  host: string;
}) => `from llama_index.core import Settings
from llama_index.core.callbacks import CallbackManager
from hanzo.llama_index import LlamaIndexCallbackHandler
 
hanzo_callback_handler = LlamaIndexCallbackHandler(
    public_key="${p.publicKey}",
    secret_key="${p.secretKey}",
    host="${p.host}"
)
Settings.callback_manager = CallbackManager([hanzo_callback_handler])`;
