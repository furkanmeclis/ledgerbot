import { RootProvider } from 'fumadocs-ui/provider/next';
import type { Metadata } from 'next';
import './global.css';

export const metadata: Metadata = {
  title: { template: '%s · Ledgerbot Docs', default: 'Ledgerbot Docs' },
  description: 'Integration and API documentation for the Ledgerbot assistant layer.',
  robots: { index: false, follow: false },
};

export default function Layout({ children }: LayoutProps<'/'>) {
  return (
    <html lang="en" suppressHydrationWarning>
      <body className="flex flex-col min-h-screen">
        <RootProvider>{children}</RootProvider>
      </body>
    </html>
  );
}
