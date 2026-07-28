#!/usr/bin/env bb
;; batch-import.bb
;;
;; Splits a prepared import file into batches and imports them sequentially.
;; Each batch includes ALL component entries (component=true) so parent-child
;; relationships resolve correctly — children can reference parents in the
;; nameTable regardless of which batch they land in.
;;
;; Usage:
;;   bb batch-import.bb \
;;     --import-file .import/imports-ready.json \
;;     --stack <stack-name> \
;;     --batch-size 100 \
;;     --out-dir .import/batches
;;
;; Then import:
;;   bb batch-import.bb \
;;     --import-file .import/imports-ready.json \
;;     --stack <stack-name> \
;;     --batch-size 100 \
;;     --out-dir .import/batches \
;;     --run
;;
;; With --run, executes `pulumi import` for each batch. Without --run, just
;; generates batch files for inspection. Pass --esc-env to wrap each import in
;; `pulumi env run <env> --` for cloud credentials.
;;
;; Options:
;;   --import-file  Path to the import JSON produced by import-id-match
;;   --stack        Pulumi stack name
;;   --batch-size   Resources per batch (default: 100)
;;   --out-dir      Directory for batch files (default: .import/batches)
;;   --run          Actually run the imports (default: dry-run, just generates files)
;;   --esc-env      ESC environment supplying cloud credentials (optional)
;;   --resume       Skip batches whose resources are already in state

(require '[cheshire.core :as json]
         '[clojure.string :as str]
         '[babashka.process :as p])

(def opts
  (let [args (partition 2 (filter #(not= "--run" %) *command-line-args*))
        m (into {} (map (fn [[k v]] [(keyword (str/replace k #"^--" "")) v]) args))]
    (assoc m :run (some #(= "--run" %) *command-line-args*))))

(def import-file (:import-file opts))
(def stack (:stack opts))
(def batch-size (parse-long (or (:batch-size opts) "100")))
(def out-dir (or (:out-dir opts) ".import/batches"))
(def esc-env (:esc-env opts))
(def run? (:run opts))
(def resume? (some #(= "--resume" %) *command-line-args*))

(when (or (nil? import-file) (nil? stack))
  (println "Usage: bb batch-import.bb --import-file <file> --stack <stack> [--batch-size 100] [--out-dir .import/batches] [--esc-env <env>] [--run] [--resume]")
  (System/exit 1))

;; Load import data
(let [data (json/parse-string (slurp import-file) true)
      resources (:resources data)
      nametable (:nameTable data)

      ;; Separate components from importable resources
      components (vec (filter :component resources))
      importable (vec (remove :component resources))

      ;; If --resume, filter out resources already in state (match by name — last URN segment)
      importable (if resume?
                   (let [_ (println "Checking existing state for --resume...")
                         state-json (-> (p/process ["pulumi" "stack" "export" "--stack" stack]
                                                   {:out :string :err :inherit})
                                        deref :out)
                         state (json/parse-string state-json true)
                         existing-names (set (map #(last (str/split (str (:urn %)) #"::"))
                                                  (:resources (:deployment state))))
                         remaining (remove #(existing-names (:name %)) importable)]
                     (println "  Already in state:" (- (count importable) (count remaining))
                              "Remaining:" (count remaining))
                     (vec remaining))
                   importable)

      batches (partition-all batch-size importable)
      n-batches (count batches)]

  (println "Import file:" import-file)
  (println "Stack:" stack)
  (println "Components:" (count components) "Importable:" (count importable))
  (println "Batch size:" batch-size "Batches:" n-batches)
  (println "Output dir:" out-dir)
  (println)

  ;; Create output directory
  (.mkdirs (java.io.File. out-dir))

  ;; Write batch files — each includes ALL components + a slice of importable resources
  (doseq [[i batch] (map-indexed vector batches)]
    (let [batch-file (str out-dir "/batch-" i ".json")
          batch-data {:resources (vec (concat components batch))
                      :nameTable nametable}]
      (spit batch-file (json/generate-string batch-data {:pretty true}))
      (println (format "Batch %d: %d resources (+ %d components) → %s"
                       i (count batch) (count components) batch-file))))

  (println)

  (if-not run?
    (do
      (println "Dry run — batch files generated. Re-run with --run to import.")
      (println "Or import manually:")
      (println (format "  for i in $(seq 0 %d); do" (dec n-batches)))
      (println (format "    %spulumi import --stack %s --file %s/batch-$i.json --yes"
                       (if esc-env (str "pulumi env run " esc-env " -- ") "") stack out-dir))
      (println "  done"))

    ;; Run imports
    (do
      (println "Importing" n-batches "batches...")
      (doseq [i (range n-batches)]
        (let [batch-file (str out-dir "/batch-" i ".json")
              _ (println (format "\n=== Batch %d/%d ===" (inc i) n-batches))
              cmd (concat (when esc-env ["pulumi" "env" "run" esc-env "--"])
                          ["pulumi" "import" "--stack" stack
                           "--file" batch-file "--yes"])
              result (p/process (vec cmd) {:out :string :err :string})
              {:keys [exit out err]} @result
              ;; The provider reference parse error is cosmetic and doesn't affect import
              provider-ref-error? (str/includes? (str err) "parse resource provider reference")
              real-exit (if (and (not (zero? exit)) provider-ref-error?
                                (str/includes? (str out) "imported"))
                          0 exit)]
          (print out) (flush)
          (binding [*out* *err*] (print err) (flush))
          (if (zero? real-exit)
            (println (format "Batch %d: SUCCESS%s" i (if provider-ref-error? " (cosmetic provider ref error ignored)" "")))
            (do
              (println (format "Batch %d: FAILED (exit %d)" i exit))
              (println "Stopping. Fix the issue and re-run with --resume to continue.")
              (System/exit 1))))))))
