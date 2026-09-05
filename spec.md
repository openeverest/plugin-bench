## How MVP is going to look like



[https://github.com/openeverest/generic-plugin-template](https://github.com/openeverest/generic-plugin-template)



- Timeline: 1 month
- Database support:

* PostgreSQL





Technologies & stack:

- Golang for backend
- React and MUI for frontend
- Kubernetes Jobs to run the runners
- Architecture should be modular allowing to add new technologies easily



Features and user stories:

- Run benchmark against database deployed with OpenEverest
  - We will have a tab in cluster overview
- Capture results in the ephemeral storage (like local pg database or sqlite)
- Users to see the history of their previous runs
  - Show all runs
  - Show runs for a specific database for comparison 
    - Maybe show some chart of how things changed over time
- A detailed view of the benchmark with information about it
- Fine tuning the parameters of the benchmark tool (decide later if we want to have it in MVP or not)







## Not in MVP, but in next stage

- Run benchmark against database deployed outside of OpenEverest
- Capture results in the external database (postgresql)
- Users can choose the plugin from the sidebar and it will show the information about prior benchmarks + allow to run a benchmark against specific database (user can choose)
