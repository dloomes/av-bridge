export default function GovukFooter() {
  return (
    <footer className="govuk-footer" role="contentinfo">
      <div className="govuk-width-container">
        <div className="govuk-footer__meta">
          <div className="govuk-footer__meta-item govuk-footer__meta-item--grow">
            <h2 className="govuk-visually-hidden">Support links</h2>
            <ul className="govuk-footer__inline-list">
              <li className="govuk-footer__inline-list-item">
                <a className="govuk-footer__link" href="/health">
                  Service health
                </a>
              </li>
            </ul>
            <span className="govuk-footer__licence-description">
              Built by Involve Visual Collaboration Ltd
            </span>
          </div>
        </div>
      </div>
    </footer>
  );
}
