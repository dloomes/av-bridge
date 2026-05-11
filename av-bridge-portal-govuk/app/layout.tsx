import type { Metadata } from 'next';
import 'govuk-frontend/dist/govuk/govuk-frontend.min.css';
import GovukHeader from '@/components/GovukHeader';
import GovukServiceNavigation from '@/components/GovukServiceNavigation';
import GovukFooter from '@/components/GovukFooter';
import GovukInit from '@/components/GovukInit';
import PhaseBanner from '@/components/PhaseBanner';
import SkipLink from '@/components/SkipLink';

export const metadata: Metadata = {
  title: 'AV Bridge | Room Management',
  description: 'Monitoring and control portal for the AV Bridge service.',
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en" className="govuk-template">
      <head>
        <meta charSet="utf-8" />
        <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover" />
        <meta name="theme-color" content="#0b0c0c" />
      </head>
      <body className="govuk-template__body">
        <GovukInit />
        <SkipLink />
        <GovukHeader />
        <GovukServiceNavigation />
        <div className="govuk-width-container">
          <PhaseBanner />
          <main className="govuk-main-wrapper" id="main-content" role="main">
            {children}
          </main>
        </div>
        <GovukFooter />
      </body>
    </html>
  );
}
