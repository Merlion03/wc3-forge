"use client";

import { useEffect, useState } from "react";
import { Button } from "@/components/ui/warcraftcn/button";

const OWNER = "StephenSHorton";
const REPO = "wc3-forge";
// GitHub redirects /releases/latest/download/<asset> to the newest release's
// matching asset, so this link is always current with no build step.
const INSTALLER_URL = `https://github.com/${OWNER}/${REPO}/releases/latest/download/wc3-forge-amd64-installer.exe`;
const RELEASES_URL = `https://github.com/${OWNER}/${REPO}/releases/latest`;

export function DownloadButton() {
  const [version, setVersion] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    fetch(`https://api.github.com/repos/${OWNER}/${REPO}/releases/latest`)
      .then((res) => (res.ok ? res.json() : null))
      .then((data) => {
        if (!cancelled && data?.tag_name) {
          setVersion(data.tag_name as string);
        }
      })
      .catch(() => {
        // Offline / rate-limited — the button still works without a version.
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <div className="flex flex-col items-center gap-2">
      <a className="wc-cursor" href={INSTALLER_URL} rel="noopener" download>
        <Button className="px-10 text-xl">
          Download for Windows{version ? ` · ${version}` : ""}
        </Button>
      </a>
      <a
        className="text-muted-foreground text-xs underline-offset-4 hover:underline"
        href={RELEASES_URL}
        rel="noopener noreferrer"
        target="_blank"
      >
        All releases & changelog
      </a>
    </div>
  );
}
