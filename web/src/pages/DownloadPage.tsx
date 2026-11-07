export function DownloadPage() {
  return (
    <>
      <header className="page-head">
        <div>
          <h1 className="page-title">Программа</h1>
          <p className="page-subtitle">
            Один файл для Windows: окно подключения, значок в трее и агент, который забирает задания.
          </p>
        </div>
      </header>

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Что нужно на компьютере</h2>
        </div>
        <ul>
          <li>Windows 10 или 11, 64-bit</li>
          <li>Видеокарта NVIDIA от 8 ГБ памяти</li>
          <li>Ollama, запущенная хотя бы один раз</li>
        </ul>
        <p className="notice-text">
          Файл пока не подписан. Windows SmartScreen может написать, что издатель неизвестен. Это ожидаемо:
          «Подробнее», затем «Выполнить в любом случае».
        </p>
        <p>
          <a className="button" data-variant="primary" href="/download/compute-node.exe">
            Скачать compute-node.exe
          </a>
        </p>
        <p className="muted">
          Если загрузка отвечает 404, файл ещё не положили рядом с панелью. Возьмите его у администратора сети
          и положите в <span className="num">/opt/compute-control/web/download/</span>.
        </p>
      </section>

      <section className="panel">
        <div className="panel-head">
          <h2 className="panel-title">Как подключиться</h2>
        </div>
        <pre className="num">{`1. В разделе «Мой узел» выпустите приглашение и скопируйте код.
2. Запустите compute-node.exe и вставьте код.
   Либо из папки с программой:
   .\\install-worker-agent.ps1 -Invite "<код>" -Binary .\\compute-node.exe
3. Дождитесь ста процентов. Узел появится в «Мой узел», в трее останется значок.`}</pre>
      </section>
    </>
  );
}
