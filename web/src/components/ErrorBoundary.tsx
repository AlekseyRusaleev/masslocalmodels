import { Component } from "react";
import type { ErrorInfo, ReactNode } from "react";

interface Props {
  children: ReactNode;
  /** Что открыто: по этому ключу граница сбрасывается при переходе. */
  resetKey: string;
}

interface State {
  error: Error | null;
}

/**
 * Граница ошибок страницы.
 *
 * Без неё любое исключение в одном разделе гасит всю панель: человек видел
 * пустой экран вместо «этот раздел сломался», а остальные данные становились
 * недоступны. Граница держит поломку внутри раздела и показывает её текстом.
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    console.error("раздел упал", error, info.componentStack);
  }

  componentDidUpdate(previous: Props): void {
    if (previous.resetKey !== this.props.resetKey && this.state.error) {
      this.setState({ error: null });
    }
  }

  render(): ReactNode {
    if (this.state.error) {
      return (
        <section className="panel">
          <div className="panel-head">
            <h2 className="panel-title">Раздел не открылся</h2>
          </div>
          <div style={{ padding: "var(--space-5)" }}>
            <p className="error-text">{this.state.error.message}</p>
            <p className="field-note">
              Остальная панель работает: ошибка осталась внутри этого раздела. Подробности — в консоли
              браузера.
            </p>
            <div className="form-actions">
              <button
                className="button"
                data-variant="primary"
                onClick={() => this.setState({ error: null })}
              >
                Попробовать снова
              </button>
            </div>
          </div>
        </section>
      );
    }
    return this.props.children;
  }
}
