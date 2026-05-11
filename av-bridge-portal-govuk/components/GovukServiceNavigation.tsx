'use client';

import Link from 'next/link';
import { usePathname } from 'next/navigation';

const navItems = [
  { href: '/', label: 'Dashboard' },
  { href: '/health', label: 'Health' },
];

export default function GovukServiceNavigation() {
  const pathname = usePathname();

  return (
    <section
      aria-label="Service information"
      className="govuk-service-navigation"
      data-module="govuk-service-navigation"
    >
      <div className="govuk-width-container">
        <div className="govuk-service-navigation__container">
          <span className="govuk-service-navigation__service-name">
            <Link href="/" className="govuk-service-navigation__link">
              AV Bridge | Room Management
            </Link>
          </span>
          <nav aria-label="Menu" className="govuk-service-navigation__wrapper">
            <button
              type="button"
              className="govuk-service-navigation__toggle govuk-js-service-navigation-toggle"
              aria-controls="navigation"
              hidden
              aria-hidden="true"
            >
              Menu
            </button>
            <ul id="navigation" className="govuk-service-navigation__list">
              {navItems.map((item) => {
                const active = pathname === item.href;
                return (
                  <li
                    key={item.href}
                    className={
                      active
                        ? 'govuk-service-navigation__item govuk-service-navigation__item--active'
                        : 'govuk-service-navigation__item'
                    }
                  >
                    <Link
                      href={item.href}
                      className="govuk-service-navigation__link"
                      {...(active ? { 'aria-current': 'true' as const } : {})}
                    >
                      {active ? (
                        <strong className="govuk-service-navigation__active-fallback">
                          {item.label}
                        </strong>
                      ) : (
                        item.label
                      )}
                    </Link>
                  </li>
                );
              })}
            </ul>
          </nav>
        </div>
      </div>
    </section>
  );
}
