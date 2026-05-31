import { SiteNav } from "@/components/site-nav";

export default function Layout({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-screen flex-col">
      <SiteNav />
      <div className="relative flex-1">{children}</div>
    </div>
  );
}
