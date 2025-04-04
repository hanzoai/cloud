import { env } from "@/src/env.mjs";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/src/components/ui/select";
import { usePostHogClientCapture } from "@/src/features/posthog-analytics/usePostHogClientCapture";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/src/components/ui/dialog";

const regions =
  env.NEXT_PUBLIC_HANZO_CLOUD_REGION === "STAGING"
    ? [
        {
          name: "STAGING",
          hostname: "staging.hanzo.ai",
          flag: "🇪🇺",
        },
      ]
    : env.NEXT_PUBLIC_HANZO_CLOUD_REGION === "DEV"
      ? [
          {
            name: "DEV",
            hostname: null,
            flag: "🚧",
          },
        ]
      : [
          {
            name: "US",
            hostname: "cloud.hanzo.ai",
            flag: "🇺🇸",
            location: "MCI",
            regionId: "us-central-1"
          },
          {
            name: "CA",
            hostname: "cloud.hanzo.ai",
            flag: "🇨🇦",
            location: "YVR",
            regionId: "ca-west-1"
          },
          {
            name: "EU",
            hostname: "cloud.hanzo.ai",
            flag: "🇪🇺",
            location: "BCN",
            regionId: "eu-west-1"
          },
        ];

export function CloudRegionSwitch({
  isSignUpPage,
}: {
  isSignUpPage?: boolean;
}) {
  const capture = usePostHogClientCapture();

  if (env.NEXT_PUBLIC_HANZO_CLOUD_REGION === undefined) return null;

  const currentRegion = regions.find(
    (region) => region.name === env.NEXT_PUBLIC_HANZO_CLOUD_REGION,
  );

  return (
    <div className="-mb-10 mt-8 rounded-lg bg-card px-6 py-6 text-sm sm:mx-auto sm:w-full sm:max-w-[480px] sm:rounded-lg sm:px-10">
      <div className="flex w-full flex-col gap-2">
        <div>
          <span className="text-sm font-medium leading-none">
            Data Region
            <DataRegionInfo />
          </span>
          {isSignUpPage && env.NEXT_PUBLIC_HANZO_CLOUD_REGION === "US" ? (
            <p className="text-xs text-muted-foreground">
              Demo project is only available in the EU region.
            </p>
          ) : null}
        </div>
        <Select
          value={currentRegion?.name}
          onValueChange={(value) => {
            const region = regions.find((region) => region.name === value);
            if (!region) return;
            capture(
              "sign_in:cloud_region_switch",
              {
                region: region.name,
              },
              {
                send_instantly: true,
              },
            );
            if (region.hostname) {
              window.location.hostname = region.hostname;
            }
          }}
        >
          <SelectTrigger className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {regions.map((region) => (
              <SelectItem key={region.name} value={region.name}>
                <span className="mr-2 text-xl leading-none">{region.flag}</span>
                {region.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    </div>
  );
}

const DataRegionInfo = () => (
  <Dialog>
    <DialogTrigger asChild>
      <a
        href="#"
        className="ml-1 text-xs text-primary-accent hover:text-hover-primary-accent"
        title="What is this?"
        tabIndex={-1}
      >
        (what is this?)
      </a>
    </DialogTrigger>
    <DialogContent>
      <DialogHeader>
        <DialogTitle>Data Regions</DialogTitle>
      </DialogHeader>
      <DialogDescription className="flex flex-col gap-2">
        <p>Hanzo Cloud is available in three data regions:</p>
        <ul className="list-disc pl-5">
          <li>US: MCI (Hanzo Cloud region us-central-1)</li>
          <li>CA: YVR (Hanzo Cloud region ca-west-1)</li>
          <li>EU: BCN (Hanzo Cloud region eu-west-1)</li>
        </ul>
        <p>
          Regions are strictly separated, and no data is shared across regions.
          Choosing a region close to you can help improve speed and comply with
          local data residency laws and privacy regulations.
        </p>
        <p>
          You can have accounts in both regions and data migrations are
          available on Team plans.
        </p>
        <p>
          For more information, visit{" "}
          <a
            href="https://hanzo.ai/docs/data-security-privacy"
            target="_blank"
            rel="noopener noreferrer"
            className="text-primary-accent underline"
          >
            hanzo.ai/security
          </a>
          .
        </p>
      </DialogDescription>
    </DialogContent>
  </Dialog>
);
